package operator

import (
	"context"
	"fmt"
	"maps"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/chrisguiney/cardano-continuum/api/v1alpha1"
)

// apply writes w. It deletes every StatefulSet p owns that is not w's (a
// different name after a tier change, or a different implementation under
// the same name), then updates w's in place or creates it. holder names
// another pool that holds w's name.
//
// A switch of implementation deletes the old StatefulSet with foreground
// propagation, so the object stays until its pod object is gone, finalizer
// included. The create under the same name then fails with AlreadyExists
// until that moment, which is the intended outcome (rule 10). A roll is an
// in-place update; the StatefulSet controller deletes the pod and creates
// its replacement only once the old object is gone (rule 9).
func (r *Pools) apply(ctx context.Context, p *v1alpha1.ContinuumPool, w Workload) (string, reconcile.Result, error) {
	owned, err := r.owned(ctx, p)
	if err != nil {
		return "", reconcile.Result{}, err
	}
	want := w.StatefulSet
	var cur *appsv1.StatefulSet
	for i := range owned {
		o := &owned[i]
		if o.Name == want.Name && o.Labels[LabelImpl] == want.Labels[LabelImpl] {
			cur = o
			continue
		}
		if err := r.drop(ctx, o); err != nil {
			return "", reconcile.Result{}, err
		}
	}
	if err := r.applyService(ctx, p, w.Service); err != nil {
		return "", reconcile.Result{}, err
	}
	if err := r.applyConfig(ctx, p, w.Config); err != nil {
		return "", reconcile.Result{}, err
	}
	switch {
	case cur == nil:
		return r.create(ctx, p, want)
	case cur.DeletionTimestamp != nil:
		return "", reconcile.Result{RequeueAfter: fenceRetry}, nil
	}
	return "", reconcile.Result{}, r.update(ctx, cur, want)
}

func (r *Pools) owned(ctx context.Context, p *v1alpha1.ContinuumPool) ([]appsv1.StatefulSet, error) {
	var list appsv1.StatefulSetList
	err := r.client.List(ctx, &list, client.InNamespace(p.Namespace), client.MatchingLabels{LabelOwner: string(p.UID)})
	if err != nil {
		return nil, fmt.Errorf("list statefulsets of pool %s: %w", p.Name, err)
	}
	return list.Items, nil
}

// create is the acquire. Nothing read before it decides whether it runs.
func (r *Pools) create(ctx context.Context, p *v1alpha1.ContinuumPool, want *appsv1.StatefulSet) (string, reconcile.Result, error) {
	err := r.client.Create(ctx, want)
	if err == nil {
		r.log.Info("workload created", "pool", p.Name, "workload", want.Name, "impl", want.Labels[LabelImpl])
		return "", reconcile.Result{}, nil
	}
	if !apierrors.IsAlreadyExists(err) {
		return "", reconcile.Result{}, fmt.Errorf("create statefulset %s: %w", want.Name, err)
	}
	// The fence is held. The read below only names the holder for the
	// status; the create has already decided.
	return r.holder(ctx, p, want), reconcile.Result{RequeueAfter: fenceRetry}, nil
}

func (r *Pools) holder(ctx context.Context, p *v1alpha1.ContinuumPool, want *appsv1.StatefulSet) string {
	var held appsv1.StatefulSet
	if err := r.client.Get(ctx, client.ObjectKeyFromObject(want), &held); err != nil {
		return ""
	}
	ref := metav1.GetControllerOf(&held)
	if ref == nil || ref.UID == p.UID {
		return ""
	}
	return ref.Name
}

// update changes cur to want in place. It carries cur's resourceVersion,
// so an update decided on a stale cache is refused with a conflict.
func (r *Pools) update(ctx context.Context, cur, want *appsv1.StatefulSet) error {
	if *cur.Spec.Replicas == *want.Spec.Replicas &&
		sameShape(want.Spec.Template.Spec, cur.Spec.Template.Spec) &&
		equality.Semantic.DeepDerivative(want.Spec.Template, cur.Spec.Template) &&
		equality.Semantic.DeepDerivative(want.Labels, cur.Labels) {
		return nil
	}
	next := cur.DeepCopy()
	next.Spec.Replicas = want.Spec.Replicas
	next.Spec.Template = want.Spec.Template
	maps.Copy(next.Labels, want.Labels)
	if err := r.client.Update(ctx, next); err != nil && !apierrors.IsConflict(err) {
		return fmt.Errorf("update statefulset %s: %w", cur.Name, err)
	}
	return nil
}

// sameShape reports whether a and b have as many containers and volumes.
// DeepDerivative treats a shorter list as a prefix of a longer one, so a
// removed container or volume, the sidecar when it moves to another pool,
// would otherwise look unchanged.
func sameShape(a, b corev1.PodSpec) bool {
	return len(a.Containers) == len(b.Containers) &&
		len(a.InitContainers) == len(b.InitContainers) &&
		len(a.Volumes) == len(b.Volumes)
}

// drop deletes a StatefulSet in the foreground, preconditioned on the UID
// and resourceVersion that were read.
func (r *Pools) drop(ctx context.Context, set *appsv1.StatefulSet) error {
	if set.DeletionTimestamp != nil {
		return nil
	}
	err := r.client.Delete(ctx, set,
		client.PropagationPolicy(metav1.DeletePropagationForeground),
		client.Preconditions{UID: &set.UID, ResourceVersion: &set.ResourceVersion})
	if err != nil && !apierrors.IsNotFound(err) && !apierrors.IsConflict(err) {
		return fmt.Errorf("delete statefulset %s: %w", set.Name, err)
	}
	r.log.Info("workload deleted", "workload", set.Name, "impl", set.Labels[LabelImpl])
	return nil
}

func (r *Pools) dropAll(ctx context.Context, p *v1alpha1.ContinuumPool) error {
	owned, err := r.owned(ctx, p)
	if err != nil {
		return err
	}
	for i := range owned {
		if err := r.drop(ctx, &owned[i]); err != nil {
			return err
		}
	}
	return nil
}

func controlledBy(obj metav1.Object, uid types.UID) bool {
	ref := metav1.GetControllerOf(obj)
	return ref != nil && ref.UID == uid
}

func (r *Pools) applyService(ctx context.Context, p *v1alpha1.ContinuumPool, want *corev1.Service) error {
	var cur corev1.Service
	err := r.client.Get(ctx, client.ObjectKeyFromObject(want), &cur)
	if apierrors.IsNotFound(err) {
		return ignoreExists(r.client.Create(ctx, want), "service", want.Name)
	}
	if err != nil {
		return fmt.Errorf("get service %s: %w", want.Name, err)
	}
	if !controlledBy(&cur, p.UID) || equality.Semantic.DeepDerivative(want.Spec, cur.Spec) {
		return nil
	}
	cur.Spec.Selector, cur.Spec.Ports = want.Spec.Selector, want.Spec.Ports
	return ignoreConflict(r.client.Update(ctx, &cur), "service", want.Name)
}

func (r *Pools) applyConfig(ctx context.Context, p *v1alpha1.ContinuumPool, want *corev1.ConfigMap) error {
	var cur corev1.ConfigMap
	err := r.client.Get(ctx, client.ObjectKeyFromObject(want), &cur)
	if apierrors.IsNotFound(err) {
		return ignoreExists(r.client.Create(ctx, want), "configmap", want.Name)
	}
	if err != nil {
		return fmt.Errorf("get configmap %s: %w", want.Name, err)
	}
	if !controlledBy(&cur, p.UID) || maps.Equal(want.Data, cur.Data) {
		return nil
	}
	cur.Data = want.Data
	maps.Copy(cur.Labels, want.Labels)
	return ignoreConflict(r.client.Update(ctx, &cur), "configmap", want.Name)
}

func ignoreExists(err error, kind, name string) error {
	if err == nil || apierrors.IsAlreadyExists(err) {
		return nil
	}
	return fmt.Errorf("create %s %s: %w", kind, name, err)
}

func ignoreConflict(err error, kind, name string) error {
	if err == nil || apierrors.IsConflict(err) {
		return nil
	}
	return fmt.Errorf("update %s %s: %w", kind, name, err)
}

// observe sets the phase and the fence condition from the pool's pods.
func (r *Pools) observe(ctx context.Context, p *v1alpha1.ContinuumPool, workload string) error {
	var pods corev1.PodList
	if err := r.client.List(ctx, &pods, client.InNamespace(p.Namespace), client.MatchingLabels{LabelOwner: string(p.UID)}); err != nil {
		return fmt.Errorf("list pods of pool %s: %w", p.Name, err)
	}
	var live *corev1.Pod
	var held []string
	for i := range pods.Items {
		pod := &pods.Items[i]
		if pod.DeletionTimestamp == nil {
			if pod.Name == PodName(workload) {
				live = pod
			}
			continue
		}
		state, err := r.nodeState(ctx, pod.Spec.NodeName)
		if err != nil {
			return err
		}
		if state == NodeUnreachable {
			held = append(held, fmt.Sprintf("pod %s on node %s", pod.Name, pod.Spec.NodeName))
		}
	}
	p.Status.Phase = PhaseOf(p, live)
	if len(held) == 0 {
		r.condition(p, CondFence, metav1.ConditionFalse, ReasonNoneHeld, "no keyed pod is held on an unreachable node")
		return nil
	}
	r.condition(p, CondFence, metav1.ConditionTrue, ReasonNodeUnreachable,
		fmt.Sprintf("%v: the node is not Ready, so the pod may still be forging; resolve the node, the operator will not", held))
	return nil
}

// nodeState reads a Node. It is a report only here; Release reads live.
func (r *Pools) nodeState(ctx context.Context, name string) (NodeState, error) {
	return readNode(ctx, r.client, name)
}

func readNode(ctx context.Context, c client.Reader, name string) (NodeState, error) {
	if name == "" {
		return NodeGone, nil
	}
	var n corev1.Node
	err := c.Get(ctx, types.NamespacedName{Name: name}, &n)
	if apierrors.IsNotFound(err) {
		return NodeGone, nil
	}
	if err != nil {
		return NodeUnreachable, fmt.Errorf("get node %s: %w", name, err)
	}
	for _, c := range n.Status.Conditions {
		if c.Type == corev1.NodeReady && c.Status == corev1.ConditionTrue {
			return NodeReady, nil
		}
	}
	return NodeUnreachable, nil
}
