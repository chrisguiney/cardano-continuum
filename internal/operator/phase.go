package operator

import (
	"math"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/chrisguiney/cardano-continuum/api/v1alpha1"
	"github.com/chrisguiney/cardano-continuum/internal/chain"
)

// PhaseOf is the phase of pool p from its pod alone: Dark, Pending (no
// Ready pod) or Syncing. Judge refines Syncing with what the pool's chain
// shows.
func PhaseOf(p *v1alpha1.ContinuumPool, pod *corev1.Pod) v1alpha1.PoolPhase {
	if p.Spec.Dark {
		return v1alpha1.PhaseDark
	}
	if pod == nil || !podReady(pod) {
		return v1alpha1.PhasePending
	}
	return v1alpha1.PhaseSyncing
}

// Facts are the network parameters a phase is judged against, read from the
// genesis. The zero Facts is "unknown": Judge then takes the best tip of the
// network's pools as the current slot and assumes defaultF.
type Facts struct {
	Start   time.Time
	SlotLen time.Duration
	// F is activeSlotsCoeff: the chance a slot has a leader.
	F float64
	// Share is the pool's fraction of the genesis stake, 0 when unknown.
	Share float64
	// Epoch is the epoch length in slots, 0 when unknown.
	Epoch uint64
	// K is the security parameter, 0 when unknown.
	K uint64
}

// defaultF is mainnet's activeSlotsCoeff, used when the genesis has none.
const defaultF = 0.05

// lagBlocks is how many expected block intervals (1/f slots each) a pool's
// tip may trail the clock. A healthy chain has no block for n slots with
// probability (1-f)^n, about e^-6 = 0.25% at 6/f slots, so a pool further
// behind than that is not at the tip.
const lagBlocks = 6

// missChance is the probability a healthy forging pool shows no block in its
// window; ln(1/missChance) = ln(1000) expected blocks are awaited.
const missChance = 0.001

func (f Facts) f() float64 {
	if f.F > 0 && f.F <= 1 {
		return f.F
	}
	return defaultF
}

// Known reports whether the genesis gave a clock to count slots by.
func (f Facts) Known() bool { return !f.Start.IsZero() && f.SlotLen > 0 }

// SlotAt is the slot current at t, 0 before the system start.
func (f Facts) SlotAt(t time.Time) uint64 {
	if !f.Known() || t.Before(f.Start) {
		return 0
	}
	return uint64(t.Sub(f.Start) / f.SlotLen)
}

// LagSlots is how far a caught-up pool's tip may trail the current slot:
// 6/f slots, never more than k/f, the span in which blocks are still
// expected to arrive within a rollback.
func (f Facts) LagSlots() uint64 {
	n := ceil(lagBlocks / f.f())
	if f.K > 0 {
		n = min(n, ceil(float64(f.K)/f.f()))
	}
	return max(n, 1)
}

// ForgeWindow is how many slots back a block counts as evidence that a pool
// forges: long enough that a pool with Share of the stake produces none with
// probability missChance, ln(1000)/(Share*f) slots, but at most one epoch.
// Without a known share the window is the epoch; without either the pool
// cannot be judged and the window is 0.
func (f Facts) ForgeWindow() uint64 {
	if f.Share <= 0 || f.Share > 1 {
		return f.Epoch
	}
	n := ceil(math.Log(1/missChance) / (f.Share * f.f()))
	if f.Epoch > 0 {
		n = min(n, f.Epoch)
	}
	return n
}

// ceil rounds up, forgiving the float error of a quotient that is an
// integer on paper (6/0.05).
func ceil(x float64) uint64 { return uint64(math.Ceil(x - 1e-9)) }

// Seen is what the operator observed of one pool's chain.
type Seen struct {
	// Tip is the pool node's newest block; HaveTip is false before any.
	Tip     chain.Mark
	HaveTip bool
	// Own is the newest block issued by the pool's own cold key.
	Own     chain.Mark
	HaveOwn bool
	// NetTip is the highest tip slot among the network's pools; it stands in
	// for the clock when the genesis is unknown.
	NetTip uint64
}

// Judge refines base, the pod's phase from PhaseOf, with the pool's chain.
//
//   - Pending and Dark stand.
//   - Syncing until the tip is within LagSlots of the current slot.
//   - Caught up, a keyed role is Forging once its own block lies within
//     ForgeWindow of the current slot, else it stays Syncing: a Ready keyed
//     pod has passed the keys gate, but only a block shows the keys work.
//   - Caught up, a pool that does not forge is Following.
//
// Pure: now is the clock.
func Judge(base v1alpha1.PoolPhase, role Role, f Facts, s Seen, now time.Time) v1alpha1.PoolPhase {
	if base != v1alpha1.PhaseSyncing || !s.HaveTip {
		return base
	}
	cur := s.NetTip
	if f.Known() {
		cur = f.SlotAt(now)
	}
	if s.Tip.Slot+f.LagSlots() < cur {
		return v1alpha1.PhaseSyncing
	}
	if !role.Keyed() {
		return v1alpha1.PhaseFollowing
	}
	w := f.ForgeWindow()
	if w == 0 || !s.HaveOwn || s.Own.Slot+w < cur {
		return v1alpha1.PhaseSyncing
	}
	return v1alpha1.PhaseForging
}

func podReady(pod *corev1.Pod) bool {
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}
