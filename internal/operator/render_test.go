package operator

import (
	"flag"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/yaml"

	"github.com/chrisguiney/cardano-continuum/api/v1alpha1"
)

var update = flag.Bool("update", false, "rewrite testdata/*.golden")

func render(t *testing.T, d NodeDriver, p *v1alpha1.ContinuumPool, img string) Workload {
	t.Helper()
	w, err := Render(d, Input{
		Network: network("ns"),
		Pool:    p,
		Peers:   []Peer{{Host: "pool-b.ns.svc.cluster.local", Port: N2NPort}},
		Image:   Image{Name: img, PullPolicy: corev1.PullIfNotPresent},
	})
	if err != nil {
		t.Fatalf("Render %s: %v", d.Impl(), err)
	}
	return w
}

// TestWorkloadNameIgnoresImplAndDigest: rule 1 of the fencing contract.
func TestWorkloadNameIgnoresImplAndDigest(t *testing.T) {
	reg := Drivers()
	var names []string
	for _, impl := range []v1alpha1.Implementation{v1alpha1.CardanoNode, v1alpha1.Dingo} {
		for _, img := range []string{"r@sha256:aaaa", "r@sha256:bbbb"} {
			for _, cr := range []string{"a", "b"} {
				p := pool("ns", cr, impl, v1alpha1.TierForging)
				p.Generation = int64(len(names) + 1)
				w := render(t, reg[impl], p, img)
				set := w.StatefulSet
				names = append(names, set.Name, set.Spec.ServiceName, w.Service.Name)
				if got := set.Spec.Template.Labels[LabelWorkload]; got != set.Name {
					t.Errorf("%s: pod workload label %q, want %q", impl, got, set.Name)
				}
			}
		}
	}
	want := Name(pool("ns", "x", v1alpha1.CardanoNode, v1alpha1.TierForging))
	if want == "" {
		t.Fatal("Name of a forging pool is empty")
	}
	for _, n := range names {
		if n != want {
			t.Fatalf("workload names %v: want every one %q", names, want)
		}
	}
}

// TestKeyedPodTemplateHasFinalizer: rule 3. The finalizer is on the
// template, so it is on the pod from its first moment.
func TestKeyedPodTemplateHasFinalizer(t *testing.T) {
	reg := Drivers()
	for _, impl := range []v1alpha1.Implementation{v1alpha1.CardanoNode, v1alpha1.Dingo} {
		forger := pool("ns", "a", impl, v1alpha1.TierForging)
		dup := pool("ns", "d", impl, v1alpha1.TierForging)
		dup.Spec.UnsafeDuplicateOf = "a"
		relay := pool("ns", "r", impl, v1alpha1.TierValidating)
		for _, tc := range []struct {
			p    *v1alpha1.ContinuumPool
			want bool
		}{{forger, true}, {dup, true}, {relay, false}} {
			tmpl := render(t, reg[impl], tc.p, "r@sha256:aaaa").StatefulSet.Spec.Template
			if got := slices.Contains(tmpl.Finalizers, Finalizer); got != tc.want {
				t.Errorf("%s %s: finalizer present %v, want %v", impl, RoleOf(tc.p), got, tc.want)
			}
			if got := hasVolumeFrom(tmpl.Spec, tc.p.Spec.KeysSecret) && tc.p.Spec.KeysSecret != ""; got != tc.want {
				t.Errorf("%s %s: keys Secret mounted %v, want %v", impl, RoleOf(tc.p), got, tc.want)
			}
		}
	}
}

func TestDuplicateNameDistinct(t *testing.T) {
	forger := pool("ns", "a", v1alpha1.CardanoNode, v1alpha1.TierForging)
	dup := pool("ns", "d", v1alpha1.CardanoNode, v1alpha1.TierForging)
	dup.Spec.UnsafeDuplicateOf = "a"
	if Name(dup) == Name(forger) {
		t.Fatalf("duplicate and forger share the name %q", Name(dup))
	}
	if PoolKey(dup.Spec.PoolID) != PoolKey(forger.Spec.PoolID) {
		t.Fatal("duplicate and forger have different pool keys")
	}
}

func TestLogicalRendersNothing(t *testing.T) {
	w, err := Render(CardanoNode{}, Input{Network: network("ns"), Pool: pool("ns", "l", v1alpha1.CardanoNode, v1alpha1.TierLogical)})
	if err != nil {
		t.Fatal(err)
	}
	if w.StatefulSet != nil || w.Service != nil || w.Config != nil {
		t.Fatalf("logical pool rendered %+v", w)
	}
}

func TestDarkRendersZeroReplicas(t *testing.T) {
	p := pool("ns", "a", v1alpha1.CardanoNode, v1alpha1.TierForging)
	p.Spec.Dark = true
	if r := render(t, CardanoNode{}, p, "r@sha256:aaaa").StatefulSet.Spec.Replicas; r == nil || *r != 0 {
		t.Fatalf("dark pool replicas %v, want 0", r)
	}
}

func TestRenderCardanoNodeRelay(t *testing.T) {
	w := render(t, CardanoNode{}, pool("ns", "r", v1alpha1.CardanoNode, v1alpha1.TierValidating), "r@sha256:aaaa")
	spec := w.StatefulSet.Spec.Template.Spec
	if slices.ContainsFunc(spec.Volumes, func(v corev1.Volume) bool { return v.Secret != nil }) {
		t.Error("relay mounts a Secret")
	}
	if len(spec.InitContainers) != 0 {
		t.Error("relay has a keys gate")
	}
	if !strings.Contains(w.Config.Data["topology.json"], "pool-b.ns.svc.cluster.local") {
		t.Errorf("topology does not list the peer: %s", w.Config.Data["topology.json"])
	}
	if !slices.ContainsFunc(spec.Volumes, func(v corev1.Volume) bool { return v.ConfigMap != nil && v.ConfigMap.Name == "genesis" }) {
		t.Error("genesis ConfigMap not mounted")
	}
	golden(t, "cardano-node-relay", w)
}

func TestRenderCardanoNodeForging(t *testing.T) {
	w := render(t, CardanoNode{}, pool("ns", "a", v1alpha1.CardanoNode, v1alpha1.TierForging), "r@sha256:aaaa")
	spec := w.StatefulSet.Spec.Template.Spec
	i := slices.IndexFunc(spec.Volumes, func(v corev1.Volume) bool { return v.Secret != nil })
	if i < 0 {
		t.Fatal("no Secret volume")
	}
	sv := spec.Volumes[i].Secret
	if sv.DefaultMode == nil || *sv.DefaultMode != 0o400 || (sv.Optional != nil && *sv.Optional) {
		t.Errorf("Secret volume mode %v optional %v, want 0400 and required", sv.DefaultMode, sv.Optional)
	}
	args := strings.Join(spec.Containers[0].Args, " ")
	for _, want := range []string{"--shelley-kes-key " + KeysDir + "/" + KESKey, "--shelley-vrf-key " + KeysDir + "/" + VRFKey, "--shelley-operational-certificate " + KeysDir + "/" + OpCert} {
		if !strings.Contains(args, want) {
			t.Errorf("args %q lack %q", args, want)
		}
	}
	for _, m := range spec.Containers[0].VolumeMounts {
		if m.MountPath == KeysDir && !m.ReadOnly {
			t.Error("keys mounted read-write")
		}
	}
	if len(spec.InitContainers) != 1 {
		t.Errorf("forger has %d init containers, want the keys gate", len(spec.InitContainers))
	}
	if len(spec.ImagePullSecrets) != 1 || spec.ImagePullSecrets[0].Name != "pull" {
		t.Errorf("pull secrets %v", spec.ImagePullSecrets)
	}
	golden(t, "cardano-node-forging", w)
}

func golden(t *testing.T, name string, w Workload) {
	t.Helper()
	got, err := yaml.Marshal(w)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join("testdata", name+".golden")
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test -update)", err)
	}
	if string(got) != string(want) {
		t.Errorf("%s differs from the golden file (go test -update rewrites it):\n%s", path, got)
	}
}

func TestDriverRegistry(t *testing.T) {
	r := Drivers()
	for _, impl := range []v1alpha1.Implementation{v1alpha1.CardanoNode, v1alpha1.Dingo} {
		d, err := r.Driver(impl)
		if err != nil {
			t.Fatalf("%s: %v", impl, err)
		}
		if d.Impl() != impl {
			t.Errorf("driver for %s renders %s", impl, d.Impl())
		}
	}
	r[v1alpha1.Amaru] = stubDriver{impl: v1alpha1.Amaru}
	for _, impl := range []v1alpha1.Implementation{v1alpha1.Amaru, v1alpha1.Rogue} {
		_, err := r.Driver(impl)
		if err == nil || !strings.Contains(err.Error(), string(impl)) {
			t.Errorf("%s: err %v, want ErrUnsupported naming it", impl, err)
		}
	}
}
