package agenda

import (
	"fmt"

	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
)

// profitScores trades and moves its stake whenever a move pays: the extra
// reward one epoch earns on its balance must exceed the move's fee. Unlike
// yield it has no patience, and it discounts a pool past saturation, whose
// reward does not grow with the stake it attracts.
func profitScores(a population.Actor, v View) []Score {
	bal := purseOf(v, a.ID)
	ss := []Score{
		{Kind: Idle, Value: 0.5, Why: "profit seeker waits"},
		pick(profile.IntentSend, 0.1, bal, "trades"),
	}
	ss = append(ss, trade(a, v, bal, false)...)
	if !v.StakeRegistered(a.ID) {
		return append(ss,
			pick(profile.IntentRegisterStake, 1, bal, "stake key not registered"),
			off(profile.IntentDelegate, "stake key not registered"),
			off(profile.IntentWithdraw, "stake key not registered"))
	}
	return append(ss,
		off(profile.IntentRegisterStake, "stake key already registered"),
		withdraw(a, v, 0.2, bal, "collect rewards promptly"),
		profitMove(a, v, bal))
}

func profitMove(a population.Actor, v View, bal purse) Score {
	ps := spreadPools(a, v, v.Pools())
	cur, delegated := v.DelegatedTo(a.ID)
	var best PoolInfo
	bestROI, curROI := -1.0, 0.0
	for _, p := range ps {
		if delegated && p.ID == cur {
			curROI = effectiveROI(p, 0)
			continue
		}
		// Ties go to the first listed, so the order of Pools decides them;
		// the chain view lists pools by ID.
		if r := effectiveROI(p, bal.balance); r > bestROI {
			best, bestROI = p, r
		}
	}
	if bestROI < 0 {
		return off(profile.IntentDelegate, "no other pool")
	}
	fee := float64(cost[profile.IntentDelegate])
	gain := (bestROI - curROI) * float64(bal.balance)
	if gain <= fee {
		return off(profile.IntentDelegate, fmt.Sprintf("gain %.0f per epoch does not cover fee %.0f", gain, fee))
	}
	s := pick(profile.IntentDelegate, min(1, gain/fee-1), bal,
		fmt.Sprintf("pool %d pays %.0f more per epoch, fee %.0f", best.ID, gain, fee))
	s.Target = best.ID
	if !delegated {
		cur = 0
	}
	return vetMove(v, s, a.ID, cur)
}

// effectiveROI is p's return per lovelace once add more lovelace joins it:
// past saturation the pool's reward is fixed, so each lovelace earns less.
func effectiveROI(p PoolInfo, add profile.Lovelace) float64 {
	total := p.Stake + add
	if p.Saturation == 0 || total <= p.Saturation {
		return p.ROI
	}
	return p.ROI * float64(p.Saturation) / float64(total)
}
