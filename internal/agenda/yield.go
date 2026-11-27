package agenda

import (
	"fmt"

	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
)

// yieldScores registers stake, delegates to the best pool, and moves only when
// another pool's ROI beats the current one by more than the actor's patience
// (in ROI units, so the same scale as PoolInfo.ROI).
func yieldScores(a population.Actor, v View) []Score {
	bal := purseOf(v, a.ID)
	ss := []Score{{Kind: Idle, Value: 1, Why: "yield farmer waits"}}
	if !v.StakeRegistered(a.ID) {
		return append(ss,
			pick(profile.IntentRegisterStake, 1, bal, "stake key not registered"),
			off(profile.IntentDelegate, "stake key not registered"),
			off(profile.IntentWithdraw, "stake key not registered"))
	}
	ss = append(ss,
		off(profile.IntentRegisterStake, "stake key already registered"),
		pick(profile.IntentWithdraw, 0.05, bal, "collect rewards"))
	return append(ss, delegateScore(a, v, bal))
}

func delegateScore(a population.Actor, v View, bal purse) Score {
	best, ok := bestPool(v.Pools())
	if !ok {
		return off(profile.IntentDelegate, "no pools")
	}
	cur, delegated := v.DelegatedTo(a.ID)
	if !delegated {
		s := pick(profile.IntentDelegate, 1, bal, "not delegated")
		s.Target = best.ID
		return vetMove(v, s, a.ID, 0)
	}
	curROI := poolROI(v.Pools(), cur)
	gap, patience := best.ROI-curROI, a.Traits.Patience
	if best.ID == cur || gap <= patience {
		return off(profile.IntentDelegate, fmt.Sprintf("gap %.4f within patience %.4f", gap, patience))
	}
	value := 1.0
	if patience > 0 {
		value = min(1, gap/patience-1)
	}
	s := pick(profile.IntentDelegate, value, bal, fmt.Sprintf("pool %d beats current by %.4f, patience %.4f", best.ID, gap, patience))
	s.Target = best.ID
	return vetMove(v, s, a.ID, cur)
}

// poolROI is the ROI of pool id, or zero if it is not listed.
func poolROI(ps []PoolInfo, id population.ActorID) float64 {
	for _, p := range ps {
		if p.ID == id {
			return p.ROI
		}
	}
	return 0
}

// bestPool returns the highest-ROI pool, the lowest ID winning ties.
func bestPool(ps []PoolInfo) (PoolInfo, bool) {
	var best PoolInfo
	found := false
	for _, p := range ps {
		if !found || p.ROI > best.ROI || (p.ROI == best.ROI && p.ID < best.ID) {
			best, found = p, true
		}
	}
	return best, found
}
