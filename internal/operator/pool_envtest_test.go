//go:build envtest

package operator

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/chrisguiney/cardano-continuum/api/v1alpha1"
)

func TestReconcileCreatesStatefulSet(t *testing.T) {
	h := newHarness(t, nil)
	h.secret("a-keys", "opcert")
	h.pool("a", v1alpha1.CardanoNode, v1alpha1.TierForging, nil)
	h.startOperator()
	p := h.get("a")
	name := Name(p)
	h.running(PodName(name), v1alpha1.CardanoNode)
	set, _ := h.set(name)
	if ref := metav1.GetControllerOf(set); ref == nil || ref.UID != p.UID {
		t.Fatalf("StatefulSet controller %v, want pool %s", ref, p.UID)
	}
	h.mustOwned(p, name)
	h.eventually("status Syncing", func() bool {
		s := h.get("a").Status
		return s.Phase == v1alpha1.PhaseSyncing && s.Workload == name && s.Image.Digest != ""
	})
	if !h.hasCondition("a", CondAdmitted, metav1.ConditionTrue, ReasonAdmitted) {
		t.Fatalf("Admitted condition %+v", h.condition("a", CondAdmitted))
	}
}

func TestDarkScalesToZero(t *testing.T) {
	h := newHarness(t, nil)
	h.secret("a-keys", "opcert")
	h.pool("a", v1alpha1.CardanoNode, v1alpha1.TierForging, nil)
	h.startOperator()
	name := Name(h.get("a"))
	h.running(PodName(name), v1alpha1.CardanoNode)
	h.edit("a", func(p *v1alpha1.ContinuumPool) { p.Spec.Dark = true })
	h.eventually("pod gone and phase Dark", func() bool {
		_, ok := h.pod(PodName(name))
		s, _ := h.set(name)
		return !ok && s != nil && *s.Spec.Replicas == 0 && h.get("a").Status.Phase == v1alpha1.PhaseDark
	})
	h.edit("a", func(p *v1alpha1.ContinuumPool) { p.Spec.Dark = false })
	h.running(PodName(name), v1alpha1.CardanoNode)
}

func TestReconcileFenceIsCondition(t *testing.T) {
	h := newHarness(t, nil)
	h.secret("a-keys", "opcert")
	h.secret("b-keys", "opcert")
	h.pool("a", v1alpha1.CardanoNode, v1alpha1.TierForging, nil)
	h.startOperator()
	name := Name(h.get("a"))
	h.running(PodName(name), v1alpha1.CardanoNode)
	h.pool("b", v1alpha1.Dingo, v1alpha1.TierForging, nil)
	h.eventually("b refused, naming a", func() bool {
		return h.hasCondition("b", CondAdmitted, metav1.ConditionFalse, ReasonFenceHeld, `"a"`)
	})
	h.consistently("a keeps its cardano-node workload", 500*time.Millisecond, func() bool {
		s, ok := h.set(name)
		return ok && s.Labels[LabelImpl] == string(v1alpha1.CardanoNode) && h.keyedPods(RoleForger) == 1
	})
}

func TestBudgetRefusalIsCondition(t *testing.T) {
	h := newHarness(t, func(n *v1alpha1.ContinuumNetwork) { n.Spec.Budget.MaxPools = 1 })
	h.pool("a", v1alpha1.CardanoNode, v1alpha1.TierValidating, nil)
	h.startOperator()
	h.running(PodName(Name(h.get("a"))), v1alpha1.CardanoNode)
	h.pool("b", v1alpha1.CardanoNode, v1alpha1.TierValidating, nil)
	h.eventually("b over budget", func() bool {
		return h.hasCondition("b", CondAdmitted, metav1.ConditionFalse, ReasonOverBudget, "pods")
	})
	if _, ok := h.set(Name(h.get("b"))); ok {
		t.Fatal("over-budget pool has a workload")
	}
	h.must(h.c.Delete(h.ctx, h.get("a")))
	h.running(PodName(Name(h.get("b"))), v1alpha1.CardanoNode)
}

func TestAmaruRefusedAsUnsupported(t *testing.T) {
	h := newHarness(t, nil)
	h.secret("a-keys", "opcert")
	h.pool("a", v1alpha1.Amaru, v1alpha1.TierForging, nil)
	h.startOperator()
	h.eventually("amaru refused", func() bool {
		return h.hasCondition("a", CondAdmitted, metav1.ConditionFalse, ReasonUnsupported, "amaru")
	})
	if _, ok := h.set(Name(h.get("a"))); ok {
		t.Fatal("amaru pool has a workload")
	}
}
