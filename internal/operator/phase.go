package operator

import (
	corev1 "k8s.io/api/core/v1"

	"github.com/chrisguiney/cardano-continuum/api/v1alpha1"
)

// PhaseOf is the phase of pool p whose workload pod is pod (nil if none).
// Forging needs the node's tip, which the operator does not read yet, so a
// ready pod reports Syncing.
func PhaseOf(p *v1alpha1.ContinuumPool, pod *corev1.Pod) v1alpha1.PoolPhase {
	if p.Spec.Dark {
		return v1alpha1.PhaseDark
	}
	if pod == nil || !podReady(pod) {
		return v1alpha1.PhasePending
	}
	return v1alpha1.PhaseSyncing
}

func podReady(pod *corev1.Pod) bool {
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}
