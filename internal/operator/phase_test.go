package operator

import (
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/chrisguiney/cardano-continuum/api/v1alpha1"
)

func TestPhaseOf(t *testing.T) {
	ready := &corev1.Pod{Status: corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}}}
	pending := &corev1.Pod{Status: corev1.PodStatus{Phase: corev1.PodPending}}
	live := pool("ns", "a", v1alpha1.CardanoNode, v1alpha1.TierForging)
	dark := pool("ns", "a", v1alpha1.CardanoNode, v1alpha1.TierForging)
	dark.Spec.Dark = true
	tests := []struct {
		name string
		p    *v1alpha1.ContinuumPool
		pod  *corev1.Pod
		want v1alpha1.PoolPhase
	}{
		{"no pod", live, nil, v1alpha1.PhasePending},
		{"pending pod", live, pending, v1alpha1.PhasePending},
		{"ready pod", live, ready, v1alpha1.PhaseSyncing},
		{"dark", dark, ready, v1alpha1.PhaseDark},
	}
	for _, tt := range tests {
		if got := PhaseOf(tt.p, tt.pod); got != tt.want {
			t.Errorf("%s: %s, want %s", tt.name, got, tt.want)
		}
	}
}
