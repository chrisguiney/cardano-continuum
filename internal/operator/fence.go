package operator

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// Release removes the fence finalizer from keyed pods whose process is
// known to have stopped (see Releasable).
//
// Unlike the pool reconciler it reads live: a Node that the cache has not
// seen yet would look deleted, and a cached status could be older than a
// container the kubelet restarted. The patch then carries the UID and
// resourceVersion read, so a release decided for one pod never lands on a
// newer pod of the same name or on a changed status (rule 6).
type Release struct {
	client client.Client
	live   client.Reader
	log    *slog.Logger
}

// NewRelease returns a release reconciler that reads pods and Nodes from
// live and patches through c.
func NewRelease(c client.Client, live client.Reader, log *slog.Logger) *Release {
	return &Release{client: c, live: live, log: log.With("component", "fence")}
}

// Reconcile releases one pod if Releasable allows it.
func (r *Release) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	var pod corev1.Pod
	if err := r.live.Get(ctx, req.NamespacedName, &pod); err != nil {
		if apierrors.IsNotFound(err) {
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, fmt.Errorf("get pod %s: %w", req.NamespacedName, err)
	}
	if pod.DeletionTimestamp == nil || !slices.Contains(pod.Finalizers, Finalizer) {
		return reconcile.Result{}, nil
	}
	state, err := readNode(ctx, r.live, pod.Spec.NodeName)
	if err != nil {
		return reconcile.Result{}, err
	}
	if !Releasable(&pod, state) {
		// Held until the kubelet reports or the Node goes; both are
		// events this controller watches. The requeue is a backstop.
		return reconcile.Result{RequeueAfter: refusalRetry}, nil
	}
	if err := r.release(ctx, &pod); err != nil {
		if apierrors.IsConflict(err) || apierrors.IsNotFound(err) {
			return reconcile.Result{RequeueAfter: time.Second}, nil
		}
		return reconcile.Result{}, err
	}
	r.log.Info("fence released", "pod", pod.Name, "uid", pod.UID, "node", pod.Spec.NodeName, "node_gone", state == NodeGone)
	return reconcile.Result{}, nil
}

// release removes the finalizer from pod with a merge patch preconditioned
// on pod's UID and resourceVersion.
func (r *Release) release(ctx context.Context, pod *corev1.Pod) error {
	rest := slices.DeleteFunc(slices.Clone(pod.Finalizers), func(f string) bool { return f == Finalizer })
	if rest == nil {
		rest = []string{}
	}
	patch, err := json.Marshal(map[string]any{"metadata": map[string]any{
		"uid":             pod.UID,
		"resourceVersion": pod.ResourceVersion,
		"finalizers":      rest,
	}})
	if err != nil {
		return fmt.Errorf("encode release patch for pod %s: %w", pod.Name, err)
	}
	if err := r.client.Patch(ctx, pod, client.RawPatch(types.MergePatchType, patch)); err != nil {
		return fmt.Errorf("release pod %s uid %s: %w", pod.Name, pod.UID, err)
	}
	return nil
}
