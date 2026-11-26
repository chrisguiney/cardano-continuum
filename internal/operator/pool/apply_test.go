package pool

import (
	"context"
	"io"
	"log/slog"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/chrisguiney/cardano-continuum/api/v1alpha1"
	"github.com/chrisguiney/cardano-continuum/internal/operator/render"
)

// writes counts the updates that reach the API server.
type writes struct {
	client.Client
	updates int
}

func (w *writes) Update(ctx context.Context, o client.Object, opts ...client.UpdateOption) error {
	w.updates++
	return w.Client.Update(ctx, o, opts...)
}

func newApplier(t *testing.T) (*applier, *writes) {
	t.Helper()
	s := runtime.NewScheme()
	if err := scheme.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	c := &writes{Client: fake.NewClientBuilder().WithScheme(s).Build()}
	return &applier{client: c, log: slog.New(slog.NewTextHandler(io.Discard, nil))}, c
}

func renderOn(t *testing.T, d render.NodeDriver, n *v1alpha1.ContinuumNetwork, p *v1alpha1.ContinuumPool) render.Workload {
	t.Helper()
	w, err := render.Render(d, render.Input{
		Network: n,
		Pool:    p,
		Peers:   []render.Peer{{Host: "pool-b.ns.svc.cluster.local", Port: render.N2NPort}},
		Image:   render.Image{Name: "r@sha256:aaaa", PullPolicy: corev1.PullIfNotPresent},
	})
	if err != nil {
		t.Fatalf("Render %s: %v", d.Impl(), err)
	}
	return w
}

func rendered(t *testing.T) render.Workload {
	t.Helper()
	p := pool("ns", "a", v1alpha1.Dingo, v1alpha1.TierValidating)
	return renderOn(t, render.Drivers()[v1alpha1.Dingo], testNetwork("ns"), p)
}

// defaulted adds what the API server fills into an object it stored.
func defaulted(set *appsv1.StatefulSet) {
	spec := &set.Spec.Template.Spec
	spec.DNSPolicy = corev1.DNSClusterFirst
	spec.SchedulerName = "default-scheduler"
	spec.RestartPolicy = corev1.RestartPolicyAlways
	spec.TerminationGracePeriodSeconds = ptr.To(int64(30))
	spec.SecurityContext = &corev1.PodSecurityContext{}
	for i := range spec.Containers {
		c := &spec.Containers[i]
		c.TerminationMessagePath = "/dev/termination-log"
		c.TerminationMessagePolicy = corev1.TerminationMessageReadFile
		for j := range c.Ports {
			c.Ports[j].Protocol = corev1.ProtocolTCP
		}
	}
	set.Spec.RevisionHistoryLimit = ptr.To(int32(10))
}

func stored(t *testing.T, r *applier, set *appsv1.StatefulSet) *appsv1.StatefulSet {
	t.Helper()
	ctx := context.Background()
	stamp(set)
	if err := r.client.Create(ctx, set); err != nil {
		t.Fatal(err)
	}
	var cur appsv1.StatefulSet
	if err := r.client.Get(ctx, client.ObjectKeyFromObject(set), &cur); err != nil {
		t.Fatal(err)
	}
	defaulted(&cur)
	return &cur
}

// TestUpdateRollsOnRemoval: dropping a field the operator rendered must reach
// the stored StatefulSet. DeepDerivative reads a missing map key, an empty
// string, a zero value and a shorter list as "unset, keep the live value".
func TestUpdateRollsOnRemoval(t *testing.T) {
	tests := map[string]struct {
		add func(*appsv1.StatefulSet)
		got func(*appsv1.StatefulSet) bool
	}{
		"env var": {
			add: func(s *appsv1.StatefulSet) {
				c := &s.Spec.Template.Spec.Containers[0]
				c.Env = append(c.Env, corev1.EnvVar{Name: "X", Value: "1"})
			},
			got: func(s *appsv1.StatefulSet) bool {
				for _, e := range s.Spec.Template.Spec.Containers[0].Env {
					if e.Name == "X" {
						return true
					}
				}
				return false
			},
		},
		"container": {
			add: func(s *appsv1.StatefulSet) {
				sp := &s.Spec.Template.Spec
				sp.Containers = append(sp.Containers, corev1.Container{Name: "side", Image: "x"})
			},
			got: func(s *appsv1.StatefulSet) bool { return len(s.Spec.Template.Spec.Containers) > 1 },
		},
		"template label": {
			add: func(s *appsv1.StatefulSet) { s.Spec.Template.Labels["extra"] = "1" },
			got: func(s *appsv1.StatefulSet) bool { _, ok := s.Spec.Template.Labels["extra"]; return ok },
		},
		"node selector key": {
			add: func(s *appsv1.StatefulSet) { s.Spec.Template.Spec.NodeSelector = map[string]string{"k": "v"} },
			got: func(s *appsv1.StatefulSet) bool { return len(s.Spec.Template.Spec.NodeSelector) > 0 },
		},
		"zeroed string": {
			add: func(s *appsv1.StatefulSet) { s.Spec.Template.Spec.ServiceAccountName = "sa" },
			got: func(s *appsv1.StatefulSet) bool { return s.Spec.Template.Spec.ServiceAccountName != "" },
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			r, c := newApplier(t)
			old := rendered(t).StatefulSet
			tc.add(old)
			cur := stored(t, r, old)
			if !tc.got(cur) {
				t.Fatal("fixture does not carry the field")
			}
			if err := r.update(context.Background(), cur, rendered(t).StatefulSet); err != nil {
				t.Fatal(err)
			}
			if c.updates != 1 {
				t.Fatalf("%d updates, want 1", c.updates)
			}
			var got appsv1.StatefulSet
			if err := c.Get(context.Background(), client.ObjectKeyFromObject(cur), &got); err != nil {
				t.Fatal(err)
			}
			if tc.got(&got) {
				t.Error("removed field still on the stored StatefulSet")
			}
		})
	}
}

// TestUpdateIgnoresServerDefaults: an object the server defaulted is not
// rewritten when nothing the operator renders changed.
func TestUpdateIgnoresServerDefaults(t *testing.T) {
	r, c := newApplier(t)
	cur := stored(t, r, rendered(t).StatefulSet)
	if err := r.update(context.Background(), cur, rendered(t).StatefulSet); err != nil {
		t.Fatal(err)
	}
	if c.updates != 0 {
		t.Fatalf("%d updates over a defaulted object, want 0", c.updates)
	}
}

func svcWith(t *testing.T, ntc bool) *corev1.Service {
	t.Helper()
	w := rendered(t)
	if ntc {
		w.Service.Spec.Ports = append(w.Service.Spec.Ports, corev1.ServicePort{Name: "ntc", Port: render.NtCPort})
	}
	return w.Service
}

func applyTwice(t *testing.T, first, second func() *corev1.Service) (*writes, corev1.Service) {
	t.Helper()
	r, c := newApplier(t)
	ctx := context.Background()
	p := pool("ns", "a", v1alpha1.Dingo, v1alpha1.TierValidating)
	svc := first()
	if err := r.applyService(ctx, p, svc); err != nil {
		t.Fatal(err)
	}
	var cur corev1.Service
	if err := c.Get(ctx, client.ObjectKeyFromObject(svc), &cur); err != nil {
		t.Fatal(err)
	}
	cur.Spec.Type = corev1.ServiceTypeClusterIP
	cur.Spec.SessionAffinity = corev1.ServiceAffinityNone
	cur.Spec.ClusterIPs = []string{corev1.ClusterIPNone}
	cur.Spec.IPFamilies = []corev1.IPFamily{corev1.IPv4Protocol}
	for i := range cur.Spec.Ports {
		cur.Spec.Ports[i].Protocol = corev1.ProtocolTCP
		cur.Spec.Ports[i].TargetPort = intstr.FromInt32(cur.Spec.Ports[i].Port)
	}
	if err := c.Client.Update(ctx, &cur); err != nil {
		t.Fatal(err)
	}
	if err := r.applyService(ctx, p, second()); err != nil {
		t.Fatal(err)
	}
	var got corev1.Service
	if err := c.Get(ctx, types.NamespacedName{Namespace: svc.Namespace, Name: svc.Name}, &got); err != nil {
		t.Fatal(err)
	}
	return c, got
}

// TestServiceRemovalPropagates: a dropped port or selector key reaches the
// stored Service.
func TestServiceRemovalPropagates(t *testing.T) {
	with := func() *corev1.Service { return svcWith(t, true) }
	without := func() *corev1.Service { return svcWith(t, false) }
	_, got := applyTwice(t, with, without)
	if len(got.Spec.Ports) != 1 {
		t.Errorf("ports = %v, want the n2n port only", got.Spec.Ports)
	}
	extra := func() *corev1.Service {
		s := svcWith(t, false)
		s.Spec.Selector["extra"] = "1"
		return s
	}
	_, got = applyTwice(t, extra, without)
	if _, ok := got.Spec.Selector["extra"]; ok {
		t.Errorf("selector = %v, removed key survived", got.Spec.Selector)
	}
}

// TestServiceIgnoresServerDefaults: defaulted Service fields cause no write.
func TestServiceIgnoresServerDefaults(t *testing.T) {
	same := func() *corev1.Service { return svcWith(t, true) }
	c, _ := applyTwice(t, same, same)
	if c.updates != 0 {
		t.Fatalf("%d updates over a defaulted Service, want 0", c.updates)
	}
}
