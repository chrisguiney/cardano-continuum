package agenda

import (
	"fmt"

	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
)

// Awards are the reward balances treasury withdrawals pay, as a treasury
// recipient sees its own.
type Awards interface {
	// Rewards is the lovelace in the actor's reward account.
	Rewards(actor population.ActorID) profile.Lovelace
}

// granteeScores is a treasury recipient's: it registers its reward account
// so a withdrawal can pay it, delegates its vote (Conway refuses a reward
// withdrawal from an account without one), withdraws each award once the
// treasury pays it, and draws its milestones as they fall due. Each step
// has its own clock, so a score of 1 means "now, when the clock fires".
func granteeScores(a population.Actor, v View) []Score {
	bal := purseOf(v, a.ID)
	ss := []Score{{Kind: Idle, Value: 1, Why: "recipient waits for an award"}}
	if !v.StakeRegistered(a.ID) {
		return append(ss, pick(profile.IntentRegisterStake, 1, bal, "registers its reward account to receive treasury awards"))
	}

	g, ok := v.(Governance)
	if !ok {
		return append(ss, off(profile.IntentDelegateDRep, "no governance state in view"))
	}

	if !g.VoteDelegated(a.ID) {
		dreps := g.DReps()
		if len(dreps) == 0 {
			return append(ss, off(profile.IntentDelegateDRep, "no DReps registered"))
		}

		s := pick(profile.IntentDelegateDRep, 1, bal, fmt.Sprintf("delegates its vote to DRep %d so it can withdraw awards", dreps[0]))
		s.Target = dreps[0]
		return append(ss, s)
	}
	return append(ss, awardScore(a, v, bal), drawScore(a, v, bal))
}

func awardScore(a population.Actor, v View, bal purse) Score {
	aw, ok := v.(Awards)
	if !ok {
		return off(profile.IntentWithdraw, "no reward balances in view")
	}

	if n := aw.Rewards(a.ID); n > 0 {
		return pick(profile.IntentWithdraw, 1, bal, fmt.Sprintf("withdraws a treasury award of %d lovelace", n))
	}
	return off(profile.IntentWithdraw, "no award paid")
}

func drawScore(a population.Actor, v View, bal purse) Score {
	p, ok := v.(Positions)
	if !ok {
		return off(profile.IntentScriptCall, "no script positions in view")
	}

	if c := p.Calls(a.ID); c != "" {
		return pick(profile.IntentScriptCall, 1, bal, "unlocks: "+c)
	}
	return off(profile.IntentScriptCall, "no milestone due")
}
