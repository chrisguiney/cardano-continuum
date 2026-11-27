package agenda

import (
	"fmt"

	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
)

// poolUpdateScore is a registered operator's standing wish to change its
// pool's parameters, against 1 for maintaining: real operators re-register
// about once a year (lab/population_fit.py, update_rate).
const poolUpdateScore = 0.05

// operatorScores registers the pool once, then maintains it, voting on the
// open proposals stake pools may vote on. Frustration
// raises the wish to leave, which runs dark first and then retire
// (pool-forging rule 18): a live pool may go dark, a dark pool whose pod
// is gone may retire, and a dark pool may come back. Going dark and
// retiring both ask the stake-floor guard first.
func operatorScores(a population.Actor, v View) []Score {
	bal := purseOf(v, a.ID)
	if !v.PoolRegistered(a.ID) {
		return []Score{
			pick(profile.IntentRegisterPool, 1, bal, "pool not registered"),
			{Kind: Idle, Value: 0.05, Why: "waiting to register"},
			off(profile.IntentRetirePool, "pool not registered"),
			off(profile.IntentWithdraw, "pool not registered"),
			off(profile.IntentUpdatePool, "pool not registered"),
			off(GoDark, "pool not registered"),
			off(GoLive, "pool not registered"),
			off(profile.IntentVote, "pool not registered"),
		}
	}
	leave := min(0.5, 0.01*a.Frustration)
	why := fmt.Sprintf("frustration %.2f", a.Frustration)
	ss := []Score{
		off(profile.IntentRegisterPool, "pool already registered"),
		{Kind: Idle, Value: 1, Why: "maintaining pool"},
		pick(profile.IntentWithdraw, 0.05, bal, "collect rewards"),
		pick(profile.IntentUpdatePool, poolUpdateScore, bal, "change pool parameters"),
	}
	if a.State == population.Retiring {
		return append(ss,
			off(GoDark, "retirement confirmed"),
			off(GoLive, "retirement confirmed: a retiring pool stays dark"),
			off(profile.IntentRetirePool, "retirement confirmed"),
			off(profile.IntentVote, "retirement confirmed"))
	}
	ss = append(ss, poolVotes(a, v, bal)...)
	pods, ok := v.(Pods)
	if !ok {
		return append(ss,
			vetStep(v, Score{Kind: GoDark, Value: leave, Why: why}, a.ID),
			off(GoLive, "no pod state in view"),
			off(profile.IntentRetirePool, "no pod state in view"))
	}
	if !pods.Dark(a.ID) {
		return append(ss,
			vetStep(v, Score{Kind: GoDark, Value: leave, Why: why}, a.ID),
			off(GoLive, "pool already live"),
			off(profile.IntentRetirePool, "pool live: go dark first"))
	}
	retire := off(profile.IntentRetirePool, "pod still running")
	if !pods.PodRunning(a.ID) {
		retire = vetStep(v, pick(profile.IntentRetirePool, leave, bal, why), a.ID)
	}
	back := 0.2 / (1 + max(a.Frustration, 0))
	return append(ss,
		off(GoDark, "pool already dark"),
		Score{Kind: GoLive, Value: back, Why: fmt.Sprintf("dark pool, frustration %.2f", a.Frustration)},
		retire)
}

// poolVotes scores a registered pool's votes: one per open proposal stake
// pools may vote on (ProposalInfo.Pools) that it has not voted on, sooner
// as the proposal nears expiry, as a DRep votes. A vote on any other
// action the ledger refuses with DisallowedVoters, so it is never offered.
func poolVotes(a population.Actor, v View, bal purse) []Score {
	g, ok := v.(Governance)
	if !ok {
		return []Score{off(profile.IntentVote, "no governance state in view")}
	}
	var ss []Score
	epoch := v.Epoch()
	for _, p := range g.OpenProposals() {
		if !p.Pools || p.Expires < epoch || g.Voted(a.ID, p.ID) {
			continue
		}
		left := p.Expires - epoch
		s := pick(profile.IntentVote, 1/float64(1+left), bal, fmt.Sprintf("%s proposal %s expires in %d epochs", p.Type, p.ID, left))
		s.Proposal = p.ID
		ss = append(ss, s)
	}
	if len(ss) == 0 {
		return []Score{off(profile.IntentVote, "no open proposal a pool may vote on")}
	}
	return ss
}
