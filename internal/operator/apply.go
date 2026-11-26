package operator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/chrisguiney/cardano-continuum/api/v1alpha1"
	"github.com/chrisguiney/cardano-continuum/internal/ident"
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
		stamp(want)
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
//
// The pod template is compared by the digest stamped on the object when it
// was last written, not field by field: the API server defaults fields the
// operator never renders, and DeepDerivative, the usual way to ignore them,
// also ignores a removed list element, a removed map key and a field set back
// to its zero value.
func (r *Pools) update(ctx context.Context, cur, want *appsv1.StatefulSet) error {
	stamp(want)
	if *cur.Spec.Replicas == *want.Spec.Replicas &&
		cur.Annotations[AnnotationSpecHash] == want.Annotations[AnnotationSpecHash] &&
		hasLabels(cur.Labels, want.Labels) {
		return nil
	}
	next := cur.DeepCopy()
	next.Spec.Replicas = want.Spec.Replicas
	next.Spec.Template = want.Spec.Template
	maps.Copy(next.Labels, want.Labels)
	if next.Annotations == nil {
		next.Annotations = map[string]string{}
	}
	next.Annotations[AnnotationSpecHash] = want.Annotations[AnnotationSpecHash]
	if err := r.client.Update(ctx, next); err != nil && !apierrors.IsConflict(err) {
		return fmt.Errorf("update statefulset %s: %w", cur.Name, err)
	}
	return nil
}

// AnnotationSpecHash records the digest of the pod template the operator
// last wrote to a StatefulSet.
const AnnotationSpecHash = ident.LabelPrefix + "/spec-hash"

// stamp records the digest of want's pod template on want.
func stamp(want *appsv1.StatefulSet) {
	b, err := json.Marshal(want.Spec.Template)
	if err != nil {
		// A PodTemplateSpec is plain data; Marshal cannot fail on it.
		panic(fmt.Sprintf("marshal pod template of %s: %v", want.Name, err))
	}
	sum := sha256.Sum256(b)
	if want.Annotations == nil {
		want.Annotations = map[string]string{}
	}
	want.Annotations[AnnotationSpecHash] = hex.EncodeToString(sum[:])
}

// hasLabels reports whether have carries every label in want with its value.
// Labels other actors add are left alone.
func hasLabels(have, want map[string]string) bool {
	for k, v := range want {
		if got, ok := have[k]; !ok || got != v {
			return false
		}
	}
	return true
}

// samePorts reports whether have serves the ports in want. An unset protocol
// is TCP and an unset target port is the port, as the API server defaults them.
func samePorts(have, want []corev1.ServicePort) bool {
	if len(have) != len(want) {
		return false
	}
	for i := range want {
		h, w := have[i], want[i]
		if h.Name != w.Name || h.Port != w.Port || h.Protocol != protocolOf(w) ||
			h.TargetPort != targetOf(w) {
			return false
		}
	}
	return true
}

func protocolOf(p corev1.ServicePort) corev1.Protocol {
	if p.Protocol == "" {
		return corev1.ProtocolTCP
	}
	return p.Protocol
}

func targetOf(p corev1.ServicePort) intstr.IntOrString {
	if p.TargetPort == (intstr.IntOrString{}) {
		return intstr.FromInt32(p.Port)
	}
	return p.TargetPort
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
	if !controlledBy(&cur, p.UID) || (maps.Equal(want.Spec.Selector, cur.Spec.Selector) && samePorts(cur.Spec.Ports, want.Spec.Ports)) {
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

// observe sets the phase and the fence condition from the pool's pods, and
// returns the workload's live pod, or nil.
func (r *Pools) observe(ctx context.Context, p *v1alpha1.ContinuumPool, workload string) (*corev1.Pod, error) {
	var pods corev1.PodList
	if err := r.client.List(ctx, &pods, client.InNamespace(p.Namespace), client.MatchingLabels{LabelOwner: string(p.UID)}); err != nil {
		return nil, fmt.Errorf("list pods of pool %s: %w", p.Name, err)
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
			return nil, err
		}
		if state == NodeUnreachable {
			held = append(held, fmt.Sprintf("pod %s on node %s", pod.Name, pod.Spec.NodeName))
		}
	}
	p.Status.Phase = PhaseOf(p, live)
	if len(held) == 0 {
		r.condition(p, CondFence, metav1.ConditionFalse, ReasonNoneHeld, "no keyed pod is held on an unreachable node")
		return live, nil
	}
	r.condition(p, CondFence, metav1.ConditionTrue, ReasonNodeUnreachable,
		fmt.Sprintf("%v: the node is not Ready, so the pod may still be forging; resolve the node, the operator will not", held))
	return live, nil
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
