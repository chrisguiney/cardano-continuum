package agenda

import (
	"fmt"

	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
)

// opportunistScores acts when the chain is busy: payments, script calls
// and mints grow with the burst above baseline, while idle stays fixed. It
// chases the best-paying pool on any gap at all.
func opportunistScores(a population.Actor, v View) []Score {
	bal := v.Balance(a.ID)
	burst := 1.0
	if m, ok := v.(Market); ok {
		burst = m.Burst()
	}
	excess := max(0, finite(burst)-1)
	why := fmt.Sprintf("activity at %.1fx baseline", burst)
	ss := []Score{
		{Kind: Idle, Value: 1, Why: "opportunist waits for a burst"},
		pick(profile.IntentSend, 0.05+excess, bal, why),
		pick(profile.IntentScriptCall, 0.5*excess, bal, why),
		pick(profile.IntentMint, 0.2*excess, bal, why),
	}
	if !v.StakeRegistered(a.ID) {
		return append(ss, pick(profile.IntentRegisterStake, 0.2, bal, "stake key not registered"))
	}
	return append(ss, chase(a, v, bal))
}

// chase moves to the highest-ROI pool whenever it beats the current one.
func chase(a population.Actor, v View, bal profile.Lovelace) Score {
	best, ok := bestPool(v.Pools())
	if !ok {
		return off(profile.IntentDelegate, "no pools")
	}
	cur, delegated := v.DelegatedTo(a.ID)
	if delegated && (best.ID == cur || best.ROI <= poolROI(v.Pools(), cur)) {
		return off(profile.IntentDelegate, "already on the best pool")
	}
	s := pick(profile.IntentDelegate, 0.5, bal, fmt.Sprintf("pool %d pays best", best.ID))
	s.Target = best.ID
	if !delegated {
		cur = 0
	}
	return vetMove(v, s, a.ID, cur)
}
