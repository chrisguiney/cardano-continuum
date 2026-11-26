package operator

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/chrisguiney/cardano-continuum/api/v1alpha1"
)

// CondGenesis is True when a network's genesis ConfigMap exists.
const CondGenesis = "GenesisReady"

// Network phases.
const (
	NetworkPending = "Pending"
	NetworkReady   = "Ready"
)

// Networks reconciles ContinuumNetwork status: whether its genesis exists,
// and the digest each implementation's pools run.
type Networks struct {
	client client.Client
	live   client.Reader
	log    *slog.Logger
}

// NewNetworks returns a network reconciler. The genesis ConfigMap is read
// from live because the cache holds only the operator's own ConfigMaps.
func NewNetworks(c client.Client, live client.Reader, log *slog.Logger) *Networks {
	return &Networks{client: c, live: live, log: log.With("component", "networks")}
}

// Reconcile refreshes one network's status.
func (r *Networks) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	var n v1alpha1.ContinuumNetwork
	if err := r.client.Get(ctx, req.NamespacedName, &n); err != nil {
		if apierrors.IsNotFound(err) {
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, fmt.Errorf("get network %s: %w", req.NamespacedName, err)
	}
	orig := n.DeepCopy()
	err := r.live.Get(ctx, types.NamespacedName{Namespace: n.Namespace, Name: n.Spec.GenesisRef}, &corev1.ConfigMap{})
	ready := err == nil
	if err != nil && !apierrors.IsNotFound(err) {
		return reconcile.Result{}, fmt.Errorf("get genesis configmap %s: %w", n.Spec.GenesisRef, err)
	}
	cond := metav1.Condition{Type: CondGenesis, Status: metav1.ConditionTrue, Reason: "Found", ObservedGeneration: n.Generation,
		Message: "genesis ConfigMap " + n.Spec.GenesisRef}
	n.Status.Phase = NetworkReady
	if !ready {
		cond.Status, cond.Reason, cond.Message = metav1.ConditionFalse, "NotFound", "genesis ConfigMap "+n.Spec.GenesisRef+" not found"
		n.Status.Phase = NetworkPending
	}
	meta.SetStatusCondition(&n.Status.Conditions, cond)
	if n.Status.Images, err = r.images(ctx, &n); err != nil {
		return reconcile.Result{}, err
	}
	var pools v1alpha1.ContinuumPoolList
	if err := r.client.List(ctx, &pools, client.InNamespace(n.Namespace)); err != nil {
		return reconcile.Result{}, fmt.Errorf("list pools of network %s: %w", n.Name, err)
	}
	n.Status.ParquetPool = ParquetSource(n.Name, n.Spec.Parquet, pools.Items)
	if err := r.client.Status().Patch(ctx, &n, client.MergeFrom(orig)); err != nil && !apierrors.IsNotFound(err) {
		return reconcile.Result{}, fmt.Errorf("patch network %s status: %w", n.Name, err)
	}
	if !ready {
		return reconcile.Result{RequeueAfter: refusalRetry}, nil
	}
	return reconcile.Result{}, nil
}

// images is the run-wide image per implementation: the one the network's
// pools record, taking the first pool by name when they differ (a roll in
// progress).
func (r *Networks) images(ctx context.Context, n *v1alpha1.ContinuumNetwork) (map[v1alpha1.Implementation]v1alpha1.ResolvedImage, error) {
	var list v1alpha1.ContinuumPoolList
	if err := r.client.List(ctx, &list, client.InNamespace(n.Namespace)); err != nil {
		return nil, fmt.Errorf("list pools of network %s: %w", n.Name, err)
	}
	slices.SortFunc(list.Items, func(a, b v1alpha1.ContinuumPool) int { return strings.Compare(a.Name, b.Name) })
	out := map[v1alpha1.Implementation]v1alpha1.ResolvedImage{}
	for _, p := range list.Items {
		_, seen := out[p.Spec.Implementation]
		if p.Spec.NetworkRef != n.Name || p.Status.Image.Digest == "" || seen {
			continue
		}
		out[p.Spec.Implementation] = p.Status.Image
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// SetupWithManager registers the network controller.
func (r *Networks) SetupWithManager(mgr manager.Manager) error {
	err := builder.ControllerManagedBy(mgr).
		Named("continuumnetwork").
		For(&v1alpha1.ContinuumNetwork{}).
		Watches(&v1alpha1.ContinuumPool{}, handler.EnqueueRequestsFromMapFunc(func(_ context.Context, o client.Object) []reconcile.Request {
			p := o.(*v1alpha1.ContinuumPool)
			return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: p.Namespace, Name: p.Spec.NetworkRef}}}
		})).
		Complete(r)
	if err != nil {
		return fmt.Errorf("set up network controller: %w", err)
	}
	return nil
}
