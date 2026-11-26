package operator

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/chrisguiney/cardano-continuum/api/v1alpha1"
	"github.com/chrisguiney/cardano-continuum/internal/operator/image"
	"github.com/chrisguiney/cardano-continuum/internal/operator/render"
)

// planner decides what workload a pool gets, or why it gets none. It only
// reads: Pools and the applier do the writing.
type planner struct {
	client   client.Client
	drivers  render.Registry
	images   image.Resolver
	defaults v1alpha1.ImageSet
	log      *slog.Logger
}

// refusal is why a pool gets no workload.
type refusal struct {
	reason, message string
}

func refuse(reason, format string, args ...any) *refusal {
	return &refusal{reason: reason, message: fmt.Sprintf(format, args...)}
}

// plan renders p's workload, or says why it gets none.
func (r *planner) plan(ctx context.Context, p *v1alpha1.ContinuumPool) (render.Workload, v1alpha1.ResolvedImage, *refusal, error) {
	var none v1alpha1.ResolvedImage
	d, err := r.drivers.Driver(p.Spec.Implementation)
	if err != nil {
		return render.Workload{}, none, refuse(ReasonUnsupported, "%v", err), nil
	}
	if render.RoleOf(p) == render.RoleNone {
		return render.Workload{}, none, nil, nil
	}
	var net v1alpha1.ContinuumNetwork
	err = r.client.Get(ctx, types.NamespacedName{Namespace: p.Namespace, Name: p.Spec.NetworkRef}, &net)
	if apierrors.IsNotFound(err) {
		return render.Workload{}, none, refuse(ReasonNetworkMissing, "network %s not found", p.Spec.NetworkRef), nil
	}
	if err != nil {
		return render.Workload{}, none, nil, fmt.Errorf("get network %s: %w", p.Spec.NetworkRef, err)
	}
	if no, err := r.keys(ctx, p); no != nil || err != nil {
		return render.Workload{}, none, no, err
	}
	sib, err := r.siblings(ctx, p, &net)
	if err != nil {
		return render.Workload{}, none, nil, err
	}
	if err := render.Admit(net.Spec.Budget, sib.claims(), p.Name); err != nil {
		return render.Workload{}, none, refuse(ReasonOverBudget, "%v", err), nil
	}
	img, no := r.resolve(ctx, p, &net)
	if no != nil {
		return render.Workload{}, none, no, nil
	}
	self := render.Member{Name: render.Name(p), PoolID: p.Spec.PoolID, Role: render.RoleOf(p)}
	w, err := render.Render(d, render.Input{
		Network: &net,
		Pool:    p,
		Peers:   render.Peers(net.Spec.Topology.Policy, p.Namespace, sib.members, self),
		Image:   render.Image{Name: pin(img.Ref, img.Digest), PullPolicy: image.PullPolicy(pin(img.Ref, img.Digest))},
		Sidecar: sib.source == p.Name,
	})
	if err != nil {
		return render.Workload{}, none, refuse(ReasonInvalid, "%v", err), nil
	}
	return w, img, nil, nil
}

// keys checks a keyed pool's Secret reference. A duplicate must name its
// own Secret, never the pool's (rule 4), and is refused, with its workload
// deleted, once that Secret is gone: the engine deletes it at scenario end.
func (r *planner) keys(ctx context.Context, p *v1alpha1.ContinuumPool) (*refusal, error) {
	role := render.RoleOf(p)
	if role.Keyed() && p.Spec.KeysSecret == "" {
		return refuse(ReasonInvalid, "a forging pool needs keysSecret"), nil
	}
	if role != render.RoleDuplicate {
		return nil, nil
	}
	var orig v1alpha1.ContinuumPool
	err := r.client.Get(ctx, types.NamespacedName{Namespace: p.Namespace, Name: p.Spec.UnsafeDuplicateOf}, &orig)
	switch {
	case apierrors.IsNotFound(err):
		return refuse(ReasonDuplicateOf, "pool %s, which this duplicates, not found", p.Spec.UnsafeDuplicateOf), nil
	case err != nil:
		return nil, fmt.Errorf("get pool %s: %w", p.Spec.UnsafeDuplicateOf, err)
	case orig.Spec.PoolID != p.Spec.PoolID:
		return refuse(ReasonInvalid, "poolId %s differs from %s of pool %s", p.Spec.PoolID, orig.Spec.PoolID, orig.Name), nil
	case orig.Spec.KeysSecret == p.Spec.KeysSecret:
		return refuse(ReasonSharedSecret, "keysSecret %s is pool %s's; a duplicate needs its own", p.Spec.KeysSecret, orig.Name), nil
	}
	secret := &metav1.PartialObjectMetadata{}
	secret.SetGroupVersionKind(corev1.SchemeGroupVersion.WithKind("Secret"))
	err = r.client.Get(ctx, types.NamespacedName{Namespace: p.Namespace, Name: p.Spec.KeysSecret}, secret)
	if apierrors.IsNotFound(err) {
		return refuse(ReasonSecretMissing, "keysSecret %s not found: the scenario is over", p.Spec.KeysSecret), nil
	}
	if err != nil {
		return nil, fmt.Errorf("get secret %s metadata: %w", p.Spec.KeysSecret, err)
	}
	return nil, nil
}

// resolve pins p's image. If the registry cannot be reached the recorded
// digest is kept, so an outage never rolls or stops a running node.
func (r *planner) resolve(ctx context.Context, p *v1alpha1.ContinuumPool, net *v1alpha1.ContinuumNetwork) (v1alpha1.ResolvedImage, *refusal) {
	ref, _ := image.Select(r.defaults, net.Spec.Images, p.Spec.Implementation, p.Spec.Image)
	if ref == "" {
		return v1alpha1.ResolvedImage{}, refuse(ReasonImageUnresolved, "no image for %s", p.Spec.Implementation)
	}
	next, err := r.images.Resolve(ctx, ref)
	if err == nil && next.Digest != "" {
		return next, nil
	}
	if cur := p.Status.Image; cur.Ref == ref && cur.Digest != "" {
		r.log.Warn(unresolvedImageWarning, "pool", p.Name, "ref", ref, "digest", cur.Digest, "err", err)
		return cur, nil
	}
	return v1alpha1.ResolvedImage{}, refuse(ReasonImageUnresolved, "resolve %s: %v", ref, err)
}

// pin is ref at digest: repository@digest, dropping any tag.
func pin(ref, digest string) string {
	if strings.Contains(ref, "@") || digest == "" {
		return ref
	}
	repo := ref
	if i := strings.LastIndex(ref, ":"); i > strings.LastIndex(ref, "/") {
		repo = ref[:i]
	}
	return repo + "@" + digest
}

// siblings are the pools of p's network that render a workload, oldest
// first, which is the budget's admission order.
type siblings struct {
	queue   []render.Claim
	source  string // the pool whose pod carries the sidecar
	members []render.Member
}

func (s siblings) claims() []render.Claim { return s.queue }

func (r *planner) siblings(ctx context.Context, p *v1alpha1.ContinuumPool, net *v1alpha1.ContinuumNetwork) (siblings, error) {
	var list v1alpha1.ContinuumPoolList
	if err := r.client.List(ctx, &list, client.InNamespace(p.Namespace)); err != nil {
		return siblings{}, fmt.Errorf("list pools in %s: %w", p.Namespace, err)
	}
	var s siblings
	s.source = render.ParquetSource(p.Spec.NetworkRef, net.Spec.Parquet, list.Items)
	pools := slices.DeleteFunc(list.Items, func(o v1alpha1.ContinuumPool) bool {
		_, err := r.drivers.Driver(o.Spec.Implementation)
		return o.Spec.NetworkRef != p.Spec.NetworkRef || render.RoleOf(&o) == render.RoleNone || err != nil || o.DeletionTimestamp != nil
	})
	slices.SortFunc(pools, func(a, b v1alpha1.ContinuumPool) int {
		if c := a.CreationTimestamp.Compare(b.CreationTimestamp.Time); c != 0 {
			return c
		}
		return strings.Compare(a.Name, b.Name)
	})
	for i := range pools {
		o := &pools[i]
		d, _ := r.drivers.Driver(o.Spec.Implementation)
		s.queue = append(s.queue, render.Claim{Name: o.Name, Demand: render.PoolDemand(d, o.Name == s.source)})
		s.members = append(s.members, render.Member{Name: render.Name(o), PoolID: o.Spec.PoolID, Role: render.RoleOf(o)})
	}
	return s, nil
}
