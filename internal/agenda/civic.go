package agenda

import (
	"fmt"

	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
)

// civicScores takes part in governance. A DRep registers, then votes on
// every open proposal it has not voted on, sooner as the proposal nears
// expiry. A wallet registers its stake and delegates its vote to one of
// the registered DReps, each equally likely.
func civicScores(a population.Actor, v View) []Score {
	bal := purseOf(v, a.ID)
	ss := []Score{
		{Kind: Idle, Value: 1, Why: "citizen waits"},
		off(profile.IntentPropose, "civic actors vote, they do not propose"),
	}
	g, ok := v.(Governance)
	if !ok {
		return append(ss, off(profile.IntentVote, "no governance state in view"))
	}
	if a.Kind == profile.DRep {
		return append(ss, drepScores(a, v, g, bal)...)
	}
	if !v.StakeRegistered(a.ID) {
		return append(ss,
			pick(profile.IntentRegisterStake, 1, bal, "stake key not registered"),
			off(profile.IntentDelegateDRep, "stake key not registered"))
	}
	if g.VoteDelegated(a.ID) {
		return append(ss, off(profile.IntentDelegateDRep, "vote already delegated"))
	}
	dreps := g.DReps()
	if len(dreps) == 0 {
		return append(ss, off(profile.IntentDelegateDRep, "no DReps registered"))
	}
	for _, d := range dreps {
		s := pick(profile.IntentDelegateDRep, 1/float64(len(dreps)), bal, fmt.Sprintf("delegate vote to DRep %d", d))
		s.Target = d
		ss = append(ss, s)
	}
	return ss
}

// drepRetireScore is a registered DRep's standing wish to deregister,
// against 1 for waiting: DReps stay registered for years.
const drepRetireScore = 0.001

func drepScores(a population.Actor, v View, g Governance, bal purse) []Score {
	if !g.DRepRegistered(a.ID) {
		return []Score{
			pick(profile.IntentRegisterDRep, 1, bal, "DRep not registered"),
			off(profile.IntentVote, "DRep not registered"),
			off(profile.IntentRetireDRep, "DRep not registered"),
		}
	}
	ss := []Score{
		off(profile.IntentRegisterDRep, "DRep already registered"),
		pick(profile.IntentRetireDRep, drepRetireScore, bal, "leave"),
	}
	epoch, open := v.Epoch(), 0
	for _, p := range g.OpenProposals() {
		if p.Expires < epoch || g.Voted(a.ID, p.ID) {
			continue
		}
		open++
		left := p.Expires - epoch
		s := pick(profile.IntentVote, 1/float64(1+left), bal, fmt.Sprintf("proposal %s expires in %d epochs", p.ID, left))
		s.Proposal = p.ID
		ss = append(ss, s)
	}
	if open == 0 {
		ss = append(ss, off(profile.IntentVote, "no open proposal left to vote on"))
	}
	return ss
}
