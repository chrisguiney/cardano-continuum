package operator

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/chrisguiney/cardano-continuum/api/v1alpha1"
	"github.com/chrisguiney/cardano-continuum/internal/chain"
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

func mark(slot uint64) chain.Mark {
	return chain.Mark{Height: slot / 20, Point: chain.Point{Slot: slot}}
}

func TestJudge(t *testing.T) {
	start := time.Unix(1_700_000_000, 0)
	// f=0.05 makes a block every 20 slots on average: the lag threshold is
	// ceil(6/0.05)=120 slots. With a 1/4 stake share the forging window is
	// ceil(ln(1000)/(0.25*0.05))=553 slots, under the 1000-slot epoch.
	f := Facts{Start: start, SlotLen: time.Second, F: 0.05, Share: 0.25, Epoch: 1000, K: 100}
	now := start.Add(2000 * time.Second)
	tip := func(slot uint64) Seen { return Seen{Tip: mark(slot), HaveTip: true} }
	own := func(s Seen, slot uint64) Seen { s.Own, s.HaveOwn = mark(slot), true; return s }
	tests := []struct {
		name string
		base v1alpha1.PoolPhase
		role Role
		f    Facts
		s    Seen
		want v1alpha1.PoolPhase
	}{
		{"not ready", v1alpha1.PhasePending, RoleForger, f, own(tip(2000), 1990), v1alpha1.PhasePending},
		{"dark", v1alpha1.PhaseDark, RoleForger, f, own(tip(2000), 1990), v1alpha1.PhaseDark},
		{"no tip yet", v1alpha1.PhaseSyncing, RoleForger, f, Seen{}, v1alpha1.PhaseSyncing},
		{"lagging", v1alpha1.PhaseSyncing, RoleForger, f, own(tip(1800), 1790), v1alpha1.PhaseSyncing},
		{"just inside the lag", v1alpha1.PhaseSyncing, RoleForger, f, own(tip(1880), 1880), v1alpha1.PhaseForging},
		{"just outside the lag", v1alpha1.PhaseSyncing, RoleForger, f, own(tip(1879), 1879), v1alpha1.PhaseSyncing},
		{"caught up, no block seen", v1alpha1.PhaseSyncing, RoleForger, f, tip(1990), v1alpha1.PhaseSyncing},
		{"caught up, block in the window", v1alpha1.PhaseSyncing, RoleForger, f, own(tip(1990), 1500), v1alpha1.PhaseForging},
		{"caught up, block before the window", v1alpha1.PhaseSyncing, RoleForger, f, own(tip(1990), 1446), v1alpha1.PhaseSyncing},
		{"block at the window edge", v1alpha1.PhaseSyncing, RoleForger, f, own(tip(1990), 1447), v1alpha1.PhaseForging},
		{"duplicate forges too", v1alpha1.PhaseSyncing, RoleDuplicate, f, own(tip(1990), 1990), v1alpha1.PhaseForging},
		{"relay caught up", v1alpha1.PhaseSyncing, RoleRelay, f, own(tip(1990), 1990), v1alpha1.PhaseFollowing},
		{"relay lagging", v1alpha1.PhaseSyncing, RoleRelay, f, tip(1000), v1alpha1.PhaseSyncing},
		{"tiny stake is capped at the epoch", v1alpha1.PhaseSyncing, RoleForger,
			Facts{Start: start, SlotLen: time.Second, F: 0.05, Share: 0.0001, Epoch: 1000}, own(tip(1990), 1000), v1alpha1.PhaseForging},
		{"tiny stake, block over an epoch ago", v1alpha1.PhaseSyncing, RoleForger,
			Facts{Start: start, SlotLen: time.Second, F: 0.05, Share: 0.0001, Epoch: 1000}, own(tip(1990), 999), v1alpha1.PhaseSyncing},
		{"no genesis: best tip is the clock", v1alpha1.PhaseSyncing, RoleForger, Facts{Epoch: 1000},
			own(Seen{Tip: mark(1990), HaveTip: true, NetTip: 2000}, 1990), v1alpha1.PhaseForging},
		{"no genesis: behind the best tip", v1alpha1.PhaseSyncing, RoleForger, Facts{Epoch: 1000},
			own(Seen{Tip: mark(1500), HaveTip: true, NetTip: 2000}, 1500), v1alpha1.PhaseSyncing},
	}
	for _, tt := range tests {
		if got := Judge(tt.base, tt.role, tt.f, tt.s, now); got != tt.want {
			t.Errorf("%s: %s, want %s", tt.name, got, tt.want)
		}
	}
}

func TestFactsWindows(t *testing.T) {
	f := Facts{F: 0.05, Share: 0.25, Epoch: 1000, K: 100}
	if got := f.LagSlots(); got != 120 {
		t.Errorf("lag %d, want 120", got)
	}
	if got := f.ForgeWindow(); got != 553 {
		t.Errorf("window %d, want 553", got)
	}
	if got := (Facts{F: 0.05, K: 3}).LagSlots(); got != 60 {
		t.Errorf("lag capped at k/f: %d, want 60", got)
	}
}
