package operator

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/chrisguiney/cardano-continuum/api/v1alpha1"
	"github.com/chrisguiney/cardano-continuum/internal/operator/image"
)

// Condition types on a ContinuumPool.
const (
	// CondAdmitted is True when the pool's workload is rendered, and False
	// with a reason when the operator refuses it.
	CondAdmitted = "Admitted"
	// CondFence is True while a keyed pod of the pool is being deleted on
	// a node that is not Ready. The finalizer holds the pod object until
	// someone resolves the node; the operator never does (rule 8).
	CondFence = "FenceHeld"
)

// Condition reasons.
const (
	ReasonAdmitted         = "Admitted"
	ReasonLogical          = "Logical"
	ReasonUnsupported      = "UnsupportedImplementation"
	ReasonNetworkMissing   = "NetworkMissing"
	ReasonOverBudget       = "OverBudget"
	ReasonInvalid          = "InvalidSpec"
	ReasonImageUnresolved  = "ImageUnresolved"
	ReasonFenceHeld        = "FenceHeld"
	ReasonSharedSecret     = "SharedSecret"
	ReasonDuplicateOf      = "DuplicateOfMissing"
	ReasonSecretMissing    = "DuplicateSecretMissing"
	ReasonNodeUnreachable  = "NodeUnreachable"
	ReasonNoneHeld         = "NoneHeld"
	refusalRetry           = 10 * time.Second
	fenceRetry             = 2 * time.Second
	unresolvedImageWarning = "image unresolved, keeping the recorded digest"
)

// Pools reconciles ContinuumPools into workloads.
//
// It never decides from what it reads whether a keyed workload may be
// created. It attempts the create, and the API server's AlreadyExists on
// the one name per PoolID is the fence (contract rules 1 and 2). Reads only
// choose between create, update and delete, and a stale read costs a
// refused write or a requeue, never a second forger.
type Pools struct {
	client   client.Client
	drivers  Registry
	images   image.Resolver
	defaults v1alpha1.ImageSet
	log      *slog.Logger
	now      func() time.Time

	mu sync.Mutex
	// protects seen
	seen map[types.UID]int64
}

// NewPools returns a pool reconciler that reads and writes through c.
func NewPools(c client.Client, drivers Registry, images image.Resolver, log *slog.Logger) *Pools {
	return &Pools{
		client:   c,
		drivers:  drivers,
		images:   images,
		defaults: drivers.Defaults(),
		log:      log.With("component", "pools"),
		now:      time.Now,
		seen:     map[types.UID]int64{},
	}
}

// refusal is why a pool gets no workload.
type refusal struct {
	reason, message string
}

func refuse(reason, format string, args ...any) *refusal {
	return &refusal{reason: reason, message: fmt.Sprintf(format, args...)}
}

// Reconcile brings one pool's workload and status to its spec.
func (r *Pools) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	var p v1alpha1.ContinuumPool
	if err := r.client.Get(ctx, req.NamespacedName, &p); err != nil {
		if apierrors.IsNotFound(err) {
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, fmt.Errorf("get pool %s: %w", req.NamespacedName, err)
	}
	if p.DeletionTimestamp != nil {
		return reconcile.Result{}, nil
	}
	if !r.newest(&p) {
		// Acting on a spec older than one already acted on would undo the
		// newer decision, and two such reconcilers flap the pool
		// (ForgingGenerationRaceBug). The cache catches up.
		return reconcile.Result{RequeueAfter: fenceRetry}, nil
	}
	orig := p.DeepCopy()
	res, err := r.converge(ctx, &p)
	if err != nil {
		return res, err
	}
	if err := r.client.Status().Patch(ctx, &p, client.MergeFrom(orig)); err != nil && !apierrors.IsNotFound(err) {
		return res, fmt.Errorf("patch pool %s status: %w", req.NamespacedName, err)
	}
	return res, nil
}

// newest records p's generation and reports whether it is at least the
// newest this reconciler has acted on for p's UID. ContinuumPool status has
// no observedGeneration to compare against, and a cache that delivers an
// older object delivers its older status with it, so the high-water mark
// is kept here. It costs one int64 per pool ever seen.
func (r *Pools) newest(p *v1alpha1.ContinuumPool) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if p.Generation < r.seen[p.UID] {
		return false
	}
	r.seen[p.UID] = p.Generation
	return true
}

func (r *Pools) converge(ctx context.Context, p *v1alpha1.ContinuumPool) (reconcile.Result, error) {
	w, img, no, err := r.plan(ctx, p)
	if err != nil {
		return reconcile.Result{}, err
	}
	if no != nil {
		r.condition(p, CondAdmitted, metav1.ConditionFalse, no.reason, no.message)
		p.Status.Workload = ""
		return reconcile.Result{RequeueAfter: refusalRetry}, r.dropAll(ctx, p)
	}
	if w.StatefulSet == nil {
		r.condition(p, CondAdmitted, metav1.ConditionTrue, ReasonLogical, "a logical pool renders nothing")
		p.Status.Workload = ""
		return reconcile.Result{}, r.dropAll(ctx, p)
	}
	holder, res, err := r.apply(ctx, p, w)
	if err != nil {
		return res, err
	}
	if holder != "" {
		r.condition(p, CondAdmitted, metav1.ConditionFalse, ReasonFenceHeld,
			fmt.Sprintf("workload %s for PoolID %s is held by pool %q", w.StatefulSet.Name, p.Spec.PoolID, holder))
		return res, nil
	}
	r.condition(p, CondAdmitted, metav1.ConditionTrue, ReasonAdmitted, "workload "+w.StatefulSet.Name)
	if roll := image.Roll(p.Status.Image, img, r.now()); roll != nil {
		p.Status.LastRoll = roll
		r.log.Info("image rolled", "pool", p.Name, "from", roll.From, "to", roll.To)
	}
	p.Status.Workload, p.Status.Image, p.Status.Build = w.StatefulSet.Name, img, img.Build
	return res, r.observe(ctx, p, w.StatefulSet.Name)
}

func (r *Pools) condition(p *v1alpha1.ContinuumPool, typ string, st metav1.ConditionStatus, reason, msg string) {
	meta.SetStatusCondition(&p.Status.Conditions, metav1.Condition{
		Type: typ, Status: st, Reason: reason, Message: msg, ObservedGeneration: p.Generation,
	})
}

// plan renders p's workload, or says why it gets none.
func (r *Pools) plan(ctx context.Context, p *v1alpha1.ContinuumPool) (Workload, v1alpha1.ResolvedImage, *refusal, error) {
	var none v1alpha1.ResolvedImage
	d, err := r.drivers.Driver(p.Spec.Implementation)
	if err != nil {
		return Workload{}, none, refuse(ReasonUnsupported, "%v", err), nil
	}
	if RoleOf(p) == RoleNone {
		return Workload{}, none, nil, nil
	}
	var net v1alpha1.ContinuumNetwork
	err = r.client.Get(ctx, types.NamespacedName{Namespace: p.Namespace, Name: p.Spec.NetworkRef}, &net)
	if apierrors.IsNotFound(err) {
		return Workload{}, none, refuse(ReasonNetworkMissing, "network %s not found", p.Spec.NetworkRef), nil
	}
	if err != nil {
		return Workload{}, none, nil, fmt.Errorf("get network %s: %w", p.Spec.NetworkRef, err)
	}
	if no, err := r.keys(ctx, p); no != nil || err != nil {
		return Workload{}, none, no, err
	}
	sib, err := r.siblings(ctx, p)
	if err != nil {
		return Workload{}, none, nil, err
	}
	if err := Admit(net.Spec.Budget, sib.claims(), p.Name); err != nil {
		return Workload{}, none, refuse(ReasonOverBudget, "%v", err), nil
	}
	img, no := r.resolve(ctx, p, &net)
	if no != nil {
		return Workload{}, none, no, nil
	}
	self := Member{Name: Name(p), PoolID: p.Spec.PoolID, Role: RoleOf(p)}
	w, err := Render(d, Input{
		Network: &net,
		Pool:    p,
		Peers:   Peers(net.Spec.Topology.Policy, p.Namespace, sib.members, self),
		Image:   Image{Name: pin(img.Ref, img.Digest), PullPolicy: image.PullPolicy(pin(img.Ref, img.Digest))},
	})
	if err != nil {
		return Workload{}, none, refuse(ReasonInvalid, "%v", err), nil
	}
	return w, img, nil, nil
}

// keys checks a keyed pool's Secret reference. A duplicate must name its
// own Secret, never the pool's (rule 4), and is refused, with its workload
// deleted, once that Secret is gone: the engine deletes it at scenario end.
func (r *Pools) keys(ctx context.Context, p *v1alpha1.ContinuumPool) (*refusal, error) {
	role := RoleOf(p)
	if role.Keyed() && p.Spec.KeysSecret == "" {
		return refuse(ReasonInvalid, "a forging pool needs keysSecret"), nil
	}
	if role != RoleDuplicate {
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
func (r *Pools) resolve(ctx context.Context, p *v1alpha1.ContinuumPool, net *v1alpha1.ContinuumNetwork) (v1alpha1.ResolvedImage, *refusal) {
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
	queue   []Claim
	members []Member
}

func (s siblings) claims() []Claim { return s.queue }

func (r *Pools) siblings(ctx context.Context, p *v1alpha1.ContinuumPool) (siblings, error) {
	var list v1alpha1.ContinuumPoolList
	if err := r.client.List(ctx, &list, client.InNamespace(p.Namespace)); err != nil {
		return siblings{}, fmt.Errorf("list pools in %s: %w", p.Namespace, err)
	}
	pools := slices.DeleteFunc(list.Items, func(o v1alpha1.ContinuumPool) bool {
		_, err := r.drivers.Driver(o.Spec.Implementation)
		return o.Spec.NetworkRef != p.Spec.NetworkRef || RoleOf(&o) == RoleNone || err != nil || o.DeletionTimestamp != nil
	})
	slices.SortFunc(pools, func(a, b v1alpha1.ContinuumPool) int {
		if c := a.CreationTimestamp.Compare(b.CreationTimestamp.Time); c != 0 {
			return c
		}
		return strings.Compare(a.Name, b.Name)
	})
	var s siblings
	for i := range pools {
		o := &pools[i]
		d, _ := r.drivers.Driver(o.Spec.Implementation)
		s.queue = append(s.queue, Claim{Name: o.Name, Demand: d.Requests()})
		s.members = append(s.members, Member{Name: Name(o), PoolID: o.Spec.PoolID, Role: RoleOf(o)})
	}
	return s, nil
}
