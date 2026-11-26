package fence

import (
	"slices"

	corev1 "k8s.io/api/core/v1"

	"github.com/chrisguiney/cardano-continuum/internal/operator/render"
)

// NodeState is what the API server says about the Node a pod is bound to.
type NodeState int

const (
	// NodeReady is a Node whose Ready condition is True.
	NodeReady NodeState = iota
	// NodeUnreachable is a Node that exists and is not Ready: NotReady or
	// Unknown. Its kubelet may still be running the pod.
	NodeUnreachable
	// NodeGone is a Node object that no longer exists, or a pod that was
	// never bound: the API server refuses to bind a pod that is being
	// deleted, so no kubelet will ever start it.
	NodeGone
)

// Releasable reports whether the fence finalizer may come off a pod. Only
// two facts allow it (contract rule 5): the pod's status, as its kubelet
// reported it, shows every container terminated, or the pod's Node is gone.
// NotReady, Unknown and elapsed time are not among them: a partitioned
// kubelet keeps its process forging. A terminal phase counts as every
// container terminated; the kubelet sets it only after they all stopped,
// including containers that never left Waiting behind a keys gate.
func Releasable(pod *corev1.Pod, node NodeState) bool {
	if pod.DeletionTimestamp == nil || !slices.Contains(pod.Finalizers, render.Finalizer) {
		return false
	}
	if node == NodeGone {
		return true
	}
	switch pod.Status.Phase {
	case corev1.PodFailed, corev1.PodSucceeded:
		return true
	}
	return allTerminated(pod)
}

func allTerminated(pod *corev1.Pod) bool {
	st := pod.Status.ContainerStatuses
	if len(st) == 0 || len(st) != len(pod.Spec.Containers) {
		return false
	}
	stopped := func(s corev1.ContainerStatus) bool { return s.State.Terminated != nil }
	running := func(s corev1.ContainerStatus) bool { return s.State.Running != nil }
	return !slices.ContainsFunc(st, func(s corev1.ContainerStatus) bool { return !stopped(s) }) &&
		!slices.ContainsFunc(pod.Status.InitContainerStatuses, running)
}
