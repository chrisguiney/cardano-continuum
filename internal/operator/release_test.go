package operator

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestReleasable(t *testing.T) {
	now := metav1.Now()
	mk := func(deleting bool, phase corev1.PodPhase, states ...corev1.ContainerState) *corev1.Pod {
		p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Finalizers: []string{Finalizer}}}
		if deleting {
			p.DeletionTimestamp = &now
		}
		p.Status.Phase = phase
		for i, s := range states {
			name := string(rune('a' + i))
			p.Spec.Containers = append(p.Spec.Containers, corev1.Container{Name: name})
			p.Status.ContainerStatuses = append(p.Status.ContainerStatuses, corev1.ContainerStatus{Name: name, State: s})
		}
		return p
	}
	running := corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}
	stopped := corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{}}
	waiting := corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{}}
	tests := []struct {
		name string
		pod  *corev1.Pod
		node NodeState
		want bool
	}{
		{"not deleting", mk(false, corev1.PodRunning, stopped), NodeGone, false},
		{"running", mk(true, corev1.PodRunning, running), NodeReady, false},
		{"one of two stopped", mk(true, corev1.PodRunning, stopped, running), NodeReady, false},
		{"all stopped", mk(true, corev1.PodRunning, stopped, stopped), NodeReady, true},
		{"no statuses", mk(true, corev1.PodPending), NodeReady, false},
		{"waiting on ready node", mk(true, corev1.PodPending, waiting), NodeReady, false},
		{"terminal phase", mk(true, corev1.PodFailed, waiting), NodeReady, true},
		{"unreachable node", mk(true, corev1.PodRunning, running), NodeUnreachable, false},
		{"node gone", mk(true, corev1.PodRunning, running), NodeGone, true},
	}
	for _, tt := range tests {
		if got := Releasable(tt.pod, tt.node); got != tt.want {
			t.Errorf("%s: %v, want %v", tt.name, got, tt.want)
		}
	}
	noFin := mk(true, corev1.PodFailed, stopped)
	noFin.Finalizers = nil
	if Releasable(noFin, NodeGone) {
		t.Error("a pod without the finalizer is releasable")
	}
}
