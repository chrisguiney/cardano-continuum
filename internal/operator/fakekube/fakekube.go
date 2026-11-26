// Package fakekube stands in for the controllers envtest does not run, so
// operator tests can watch pods come and go against a real API server:
//
//   - the StatefulSet controller, for one-replica sets: it creates pod
//     <set>-0 from the template and deletes it when the set is scaled to
//     zero or its template changes;
//   - the garbage collector: foreground deletion of a StatefulSet, and
//     deletion of pods whose owning set is gone;
//   - pod GC: a force delete of pods bound to a Node that no longer exists;
//   - one kubelet, serving one Node: it starts a pod when its gate allows,
//     reports every container terminated when the pod is deleted, then makes
//     the final delete.
//
// Tests steer it: Withhold keeps a deleted pod terminating, Lose cuts the
// kubelet off from its node. The finalizer logic under test is never part
// of the fake.
package fakekube

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// hashAnnotation records the template a pod was created from.
const hashAnnotation = "fakekube/template-hash"

const foreground = metav1.FinalizerDeleteDependents

// Gate decides whether the kubelet may start a pod.
type Gate func(ctx context.Context, pod *corev1.Pod) bool

// Cluster is the fake. Create it with New and drive it with Run.
type Cluster struct {
	c    client.Client
	ns   string
	node string
	gate Gate
	tick time.Duration

	mu sync.Mutex
	// protects withheld, lost
	withheld map[string]bool
	lost     bool
}

// New returns a fake for namespace ns whose kubelet serves Node node. A nil
// gate starts every pod whose Secret volumes all exist and hold no empty
// entry (see SecretsGate).
func New(c client.Client, ns, node string, gate Gate) *Cluster {
	k := &Cluster{c: c, ns: ns, node: node, gate: gate, tick: 20 * time.Millisecond, withheld: map[string]bool{}}
	if k.gate == nil {
		k.gate = SecretsGate(c)
	}
	return k
}

// Withhold makes the kubelet stop (on) or resume (off) reporting pod's
// termination, so a deleted pod stays terminating.
func (k *Cluster) Withhold(pod string, on bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.withheld[pod] = on
}

// Lose cuts the kubelet off: its pods keep their status and are never
// finalised, as on a partitioned node.
func (k *Cluster) Lose() {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.lost = true
}

func (k *Cluster) isWithheld(pod string) bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.withheld[pod]
}

func (k *Cluster) isLost() bool {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.lost
}

// Run creates the Node, then steps every controller each tick until ctx is
// cancelled. It returns nil on cancellation.
func (k *Cluster) Run(ctx context.Context) error {
	if err := k.register(ctx); err != nil && ctx.Err() == nil {
		return err
	}
	t := time.NewTicker(k.tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
		if err := k.step(ctx); err != nil && ctx.Err() == nil {
			return err
		}
	}
}

// register creates the kubelet's Node and marks it Ready.
func (k *Cluster) register(ctx context.Context) error {
	node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: k.node}}
	if err := k.c.Create(ctx, node); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create node %s: %w", k.node, err)
	}
	return SetReady(ctx, k.c, k.node, true)
}

// SetReady sets Node name's Ready condition.
func SetReady(ctx context.Context, c client.Client, name string, ready bool) error {
	var n corev1.Node
	if err := c.Get(ctx, types.NamespacedName{Name: name}, &n); err != nil {
		return fmt.Errorf("get node %s: %w", name, err)
	}
	st := corev1.ConditionFalse
	if ready {
		st = corev1.ConditionTrue
	}
	n.Status.Conditions = []corev1.NodeCondition{{Type: corev1.NodeReady, Status: st, LastTransitionTime: metav1.Now()}}
	if err := c.Status().Update(ctx, &n); err != nil {
		return fmt.Errorf("set node %s ready=%v: %w", name, ready, err)
	}
	return nil
}

func (k *Cluster) step(ctx context.Context) error {
	var sets appsv1.StatefulSetList
	if err := k.c.List(ctx, &sets, client.InNamespace(k.ns)); err != nil {
		return fmt.Errorf("list statefulsets: %w", err)
	}
	var pods corev1.PodList
	if err := k.c.List(ctx, &pods, client.InNamespace(k.ns)); err != nil {
		return fmt.Errorf("list pods: %w", err)
	}
	byName := map[string]*corev1.Pod{}
	for i := range pods.Items {
		byName[pods.Items[i].Name] = &pods.Items[i]
	}
	var errs []error
	for i := range sets.Items {
		errs = append(errs, k.statefulSet(ctx, &sets.Items[i], byName[sets.Items[i].Name+"-0"]))
	}
	for i := range pods.Items {
		errs = append(errs, k.orphan(ctx, &pods.Items[i], sets.Items))
		errs = append(errs, k.kubelet(ctx, &pods.Items[i]))
	}
	return benign(errors.Join(errs...))
}

// benign drops the errors a racing controller expects.
func benign(err error) error {
	if err == nil {
		return nil
	}
	var out []error
	for _, e := range unwrapJoin(err) {
		if !apierrors.IsNotFound(e) && !apierrors.IsConflict(e) && !apierrors.IsAlreadyExists(e) {
			out = append(out, e)
		}
	}
	return errors.Join(out...)
}

func unwrapJoin(err error) []error {
	if j, ok := err.(interface{ Unwrap() []error }); ok {
		return j.Unwrap()
	}
	return []error{err}
}

func (k *Cluster) statefulSet(ctx context.Context, set *appsv1.StatefulSet, pod *corev1.Pod) error {
	if set.DeletionTimestamp != nil {
		return k.foreground(ctx, set, pod)
	}
	want := set.Spec.Replicas == nil || *set.Spec.Replicas > 0
	switch {
	case pod == nil && want:
		return k.c.Create(ctx, k.podFor(set))
	case pod == nil || !ownedBy(pod, set.UID) || pod.DeletionTimestamp != nil:
		return nil
	case !want || pod.Annotations[hashAnnotation] != templateHash(set):
		return k.c.Delete(ctx, pod, client.Preconditions{UID: &pod.UID})
	}
	return nil
}

// foreground is the garbage collector's half of a foreground delete: delete
// the dependents, and drop the owner's finalizer once none is left.
func (k *Cluster) foreground(ctx context.Context, set *appsv1.StatefulSet, pod *corev1.Pod) error {
	if !slices.Contains(set.Finalizers, foreground) {
		return nil
	}
	if pod != nil && ownedBy(pod, set.UID) {
		if pod.DeletionTimestamp == nil {
			return k.c.Delete(ctx, pod, client.Preconditions{UID: &pod.UID})
		}
		return nil
	}
	set.Finalizers = slices.DeleteFunc(set.Finalizers, func(f string) bool { return f == foreground })
	return k.c.Update(ctx, set)
}

// orphan deletes a pod whose owning set is gone (background GC) and force
// deletes a pod bound to a Node that is gone (pod GC).
func (k *Cluster) orphan(ctx context.Context, pod *corev1.Pod, sets []appsv1.StatefulSet) error {
	ref := metav1.GetControllerOf(pod)
	if pod.DeletionTimestamp == nil && ref != nil && !slices.ContainsFunc(sets, func(s appsv1.StatefulSet) bool { return s.UID == ref.UID }) {
		return k.c.Delete(ctx, pod, client.Preconditions{UID: &pod.UID})
	}
	if pod.Spec.NodeName == "" {
		return nil
	}
	err := k.c.Get(ctx, types.NamespacedName{Name: pod.Spec.NodeName}, &corev1.Node{})
	if !apierrors.IsNotFound(err) {
		return err
	}
	return k.c.Delete(ctx, pod, client.GracePeriodSeconds(0), client.Preconditions{UID: &pod.UID})
}

func (k *Cluster) kubelet(ctx context.Context, pod *corev1.Pod) error {
	if pod.Spec.NodeName != k.node || k.isLost() {
		return nil
	}
	if pod.DeletionTimestamp == nil {
		return k.start(ctx, pod)
	}
	if k.isWithheld(pod.Name) {
		return nil
	}
	if pod.Status.Phase != corev1.PodFailed {
		pod.Status = stopped(pod)
		return k.c.Status().Update(ctx, pod)
	}
	return k.c.Delete(ctx, pod, client.GracePeriodSeconds(0), client.Preconditions{UID: &pod.UID})
}

func (k *Cluster) start(ctx context.Context, pod *corev1.Pod) error {
	if pod.Status.Phase == corev1.PodRunning {
		return nil
	}
	if !k.gate(ctx, pod) {
		if pod.Status.Phase == corev1.PodPending && len(pod.Status.ContainerStatuses) > 0 {
			return nil
		}
		pod.Status = waiting(pod)
		return k.c.Status().Update(ctx, pod)
	}
	pod.Status = running(pod)
	return k.c.Status().Update(ctx, pod)
}

// SecretsGate starts a pod only if every Secret it mounts exists and has no
// empty entry. A blank keys Secret has empty entries.
func SecretsGate(c client.Client) Gate {
	return func(ctx context.Context, pod *corev1.Pod) bool {
		for _, v := range pod.Spec.Volumes {
			if v.Secret == nil {
				continue
			}
			var s corev1.Secret
			if err := c.Get(ctx, types.NamespacedName{Namespace: pod.Namespace, Name: v.Secret.SecretName}, &s); err != nil {
				return false
			}
			if slices.ContainsFunc(mapValues(s.Data), func(b []byte) bool { return len(b) == 0 }) {
				return false
			}
		}
		return true
	}
}

func mapValues(m map[string][]byte) [][]byte {
	out := make([][]byte, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	return out
}

func (k *Cluster) podFor(set *appsv1.StatefulSet) *corev1.Pod {
	t := set.Spec.Template.DeepCopy()
	ann := map[string]string{hashAnnotation: templateHash(set)}
	for key, v := range t.Annotations {
		ann[key] = v
	}
	spec := t.Spec
	spec.NodeName = k.node
	spec.Hostname = set.Name + "-0"
	spec.Subdomain = set.Spec.ServiceName
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: set.Name + "-0", Namespace: set.Namespace,
			Labels: t.Labels, Annotations: ann, Finalizers: t.Finalizers,
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(set, appsv1.SchemeGroupVersion.WithKind("StatefulSet"))},
		},
		Spec: spec,
	}
}

func templateHash(set *appsv1.StatefulSet) string {
	b, _ := json.Marshal(set.Spec.Template)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:8])
}

func ownedBy(pod *corev1.Pod, uid types.UID) bool {
	ref := metav1.GetControllerOf(pod)
	return ref != nil && ref.UID == uid
}

func statuses(pod *corev1.Pod, st corev1.ContainerState) []corev1.ContainerStatus {
	out := make([]corev1.ContainerStatus, 0, len(pod.Spec.Containers))
	for _, c := range pod.Spec.Containers {
		out = append(out, corev1.ContainerStatus{Name: c.Name, Image: c.Image, State: st, Ready: st.Running != nil})
	}
	return out
}

func running(pod *corev1.Pod) corev1.PodStatus {
	now := metav1.Now()
	return corev1.PodStatus{
		Phase:             corev1.PodRunning,
		Conditions:        []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue, LastTransitionTime: now}},
		ContainerStatuses: statuses(pod, corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: now}}),
		StartTime:         &now,
	}
}

func waiting(pod *corev1.Pod) corev1.PodStatus {
	return corev1.PodStatus{
		Phase:             corev1.PodPending,
		ContainerStatuses: statuses(pod, corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "PodInitializing"}}),
	}
}

func stopped(pod *corev1.Pod) corev1.PodStatus {
	now := metav1.Now()
	return corev1.PodStatus{
		Phase:             corev1.PodFailed,
		ContainerStatuses: statuses(pod, corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 143, FinishedAt: now}}),
	}
}
