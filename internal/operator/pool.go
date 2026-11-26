package operator

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/chrisguiney/cardano-continuum/api/v1alpha1"
	"github.com/chrisguiney/cardano-continuum/internal/operator/image"
	"github.com/chrisguiney/cardano-continuum/internal/operator/render"
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
	client client.Client
	log    *slog.Logger
	now    func() time.Time
	// watch refines Syncing with the pool's chain; nil leaves a Ready pod
	// Syncing.
	watch   *Watch
	plans   planner
	writes  applier
	reloads reloader

	mu sync.Mutex
	// protects seen
	seen map[types.UID]int64
}

// NewPools returns a pool reconciler that reads and writes through c. With
// a nil exec running nodes are never told to reread their Config.
func NewPools(c client.Client, drivers render.Registry, images image.Resolver, exec Exec, log *slog.Logger) *Pools {
	log = log.With("component", "pools")
	return &Pools{
		client:  c,
		log:     log,
		now:     time.Now,
		plans:   planner{client: c, drivers: drivers, images: images, defaults: drivers.Defaults(), log: log},
		writes:  applier{client: c, log: log},
		reloads: reloader{client: c, exec: exec, log: log, now: time.Now},
		seen:    map[types.UID]int64{},
	}
}

// WithWatch makes r judge Forging and Following from w and returns r.
func (r *Pools) WithWatch(w *Watch) *Pools {
	r.watch = w
	return r
}

// Reconcile brings one pool's workload and status to its spec.
func (r *Pools) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	var p v1alpha1.ContinuumPool
	if err := r.client.Get(ctx, req.NamespacedName, &p); err != nil {
		if apierrors.IsNotFound(err) {
			r.forgetNamed(req)
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, fmt.Errorf("get pool %s: %w", req.NamespacedName, err)
	}
	if p.DeletionTimestamp != nil {
		r.forget(&p)
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
	w, img, no, err := r.plans.plan(ctx, p)
	if err != nil {
		return reconcile.Result{}, err
	}
	if no != nil {
		setCondition(p, CondAdmitted, metav1.ConditionFalse, no.reason, no.message)
		p.Status.Workload = ""
		r.forget(p)
		return reconcile.Result{RequeueAfter: refusalRetry}, r.writes.dropAll(ctx, p)
	}
	if w.StatefulSet == nil {
		setCondition(p, CondAdmitted, metav1.ConditionTrue, ReasonLogical, "a logical pool renders nothing")
		p.Status.Workload = ""
		r.forget(p)
		return reconcile.Result{}, r.writes.dropAll(ctx, p)
	}
	holder, res, err := r.writes.apply(ctx, p, w)
	if err != nil {
		return res, err
	}
	if holder != "" {
		setCondition(p, CondAdmitted, metav1.ConditionFalse, ReasonFenceHeld,
			fmt.Sprintf("workload %s for PoolID %s is held by pool %q", w.StatefulSet.Name, p.Spec.PoolID, holder))
		r.forget(p)
		return res, nil
	}
	setCondition(p, CondAdmitted, metav1.ConditionTrue, ReasonAdmitted, "workload "+w.StatefulSet.Name)
	if roll := image.Roll(p.Status.Image, img, r.now()); roll != nil {
		p.Status.LastRoll = roll
		r.log.Info("image rolled", "pool", p.Name, "from", roll.From, "to", roll.To)
	}
	p.Status.Workload, p.Status.Image, p.Status.Build = w.StatefulSet.Name, img, img.Build
	live, err := r.writes.observe(ctx, p, w.StatefulSet.Name)
	if err != nil {
		return res, err
	}
	res = sooner(res, r.chains(ctx, p, live))
	wait, err := r.reloads.reload(ctx, live, w)
	return sooner(res, wait), err
}

func (r *Pools) forget(p *v1alpha1.ContinuumPool) {
	if r.watch != nil {
		r.watch.Forget(p)
	}
}

func (r *Pools) forgetNamed(req reconcile.Request) {
	if r.watch != nil {
		r.watch.ForgetNamed(req.Namespace, req.Name)
	}
}

// chains refines a Syncing pod's phase with what its chain shows, records
// its tip and newest own block, and returns when to look again. A pod that
// is not Ready, or a pool that is Dark, is no longer followed.
func (r *Pools) chains(ctx context.Context, p *v1alpha1.ContinuumPool, pod *corev1.Pod) time.Duration {
	if r.watch == nil {
		return 0
	}
	if p.Status.Phase != v1alpha1.PhaseSyncing || pod == nil {
		r.watch.Forget(p)
		return 0
	}
	var net v1alpha1.ContinuumNetwork
	if err := r.client.Get(ctx, types.NamespacedName{Namespace: p.Namespace, Name: p.Spec.NetworkRef}, &net); err != nil {
		r.log.Warn("network for chain phase", "pool", p.Name, "err", err)
		return r.watch.Poll()
	}
	o := r.watch.Observe(ctx, p, &net, pod)
	if o.Seen.HaveTip {
		p.Status.Tip = tipStatus(o.Seen.Tip)
	}
	p.Status.LastBlock = o.Last
	p.Status.Phase = r.watch.Phase(p.Status.Phase, p, o)
	return r.watch.Poll()
}

// setCondition sets condition typ on p, stamped with p's generation.
func setCondition(p *v1alpha1.ContinuumPool, typ string, st metav1.ConditionStatus, reason, msg string) {
	meta.SetStatusCondition(&p.Status.Conditions, metav1.Condition{
		Type: typ, Status: st, Reason: reason, Message: msg, ObservedGeneration: p.Generation,
	})
}

// SetupWithManager registers the pool controller.
func (r *Pools) SetupWithManager(mgr manager.Manager) error {
	c := mgr.GetClient()
	err := builder.ControllerManagedBy(mgr).
		Named("continuumpool").
		For(&v1alpha1.ContinuumPool{}).
		Owns(&appsv1.StatefulSet{}).
		Watches(&corev1.Pod{}, handler.EnqueueRequestsFromMapFunc(poolOfObject)).
		Watches(&v1alpha1.ContinuumPool{}, handler.EnqueueRequestsFromMapFunc(siblingsOf(c)),
			builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(&v1alpha1.ContinuumNetwork{}, handler.EnqueueRequestsFromMapFunc(poolsOfNetwork(c))).
		Watches(&corev1.Node{}, handler.EnqueueRequestsFromMapFunc(poolsOnNode(c))).
		WatchesMetadata(&corev1.Secret{}, handler.EnqueueRequestsFromMapFunc(poolsOfSecret(c))).
		Complete(r)
	if err != nil {
		return fmt.Errorf("set up pool controller: %w", err)
	}
	return nil
}
