package pace

import (
	"maps"
	"math/rand/v2"
	"slices"

	"github.com/chrisguiney/cardano-continuum/internal/agenda"
	"github.com/chrisguiney/cardano-continuum/internal/build"
	"github.com/chrisguiney/cardano-continuum/internal/chain"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/wire"
	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
)

// State is an actor's wake-up state, held by the cohort.
type State = wire.Pace

// Clock is one of a population actor's per-intent Poisson clocks.
type Clock = wire.Clock

// Subject is an actor as the pace sees it: its wake-up state, the profile
// and agenda that shape it, and its stream of draws.
type Subject struct {
	*State
	Prof   *profile.Profile
	Policy agenda.Policy
	// Rate is the sum of the profile's base rates, before RateMult.
	Rate float64
	Drawer
}

// Drawer is an actor's counter of random draws.
type Drawer interface {
	// Draw returns a fresh stream for the actor's next random decision.
	Draw(a population.Actor, purpose string) *rand.Rand
}

// Chain is the chain view the pace reads registrations from.
type Chain interface {
	// View is nil until the first block, so a genesis actor finds none.
	View() *chain.View
}

// Flight reports unfinished intents.
type Flight interface {
	// InFlight reports whether an unfinished intent of the actor has kind.
	InFlight(id population.ActorID, kind string) bool
}

// Agenda is the agenda's view of the chain.
type Agenda interface {
	Agenda() agenda.View
}

// bootRate is how often (per second) an unregistered population actor
// wakes to register: every ten minutes, a few blocks of a devnet.
const bootRate = 1.0 / 600

// Pop is the population actors' clocks. A population actor does not wake at
// one rate and let its agenda choose among everything: that would make each
// intent's rate depend on what else the agenda offers. It runs one Poisson
// clock per intent at the fitted rate (a superposition: wake at the sum,
// then draw which clock fired), and the agenda only says whether the fired
// intent is possible now. Until it is registered it wakes at bootRate to
// register instead, since a pool or DRep that waited a fitted lifetime to
// register would never exist.
type Pop struct {
	spec Spec
	env  Env
}

func newPop(spec Spec, env Env) *Pop { return &Pop{spec: spec, env: env} }

// Clocks are a population actor's intent clocks, in kind order, and the
// intent it registers with; ok is false for an actor of no population.
func (pp *Pop) Clocks(a population.Actor, p profile.Profile) (cs []Clock, boot profile.IntentKind, ok bool) {
	if p.Name == pp.spec.Proposer && len(pp.spec.Proposals) > 0 {
		return proposerClocks(p.Rates.Base[profile.IntentPropose], pp.spec.Proposals), profile.IntentRegisterStake, true
	}
	pop, found := pp.spec.Population(p.Name)
	if !found || pop.Count == 0 {
		return nil, "", false
	}
	for _, k := range slices.Sorted(maps.Keys(p.Rates.Base)) {
		c := Clock{Kind: k, Rate: p.Rates.Base[k]}
		if k == profile.IntentVote && pop.DRep != nil {
			// Only Voting of the DReps vote at all, so each voter votes at
			// the population's rate over that share.
			if pop.DRep.Voting <= 0 || a.Stream("voter").Float64() >= voterShare(pop) {
				continue
			}
			c.Rate /= pop.DRep.Voting
		}
		cs = append(cs, c)
	}
	boot = profile.IntentRegisterPool
	if pop.DRep != nil {
		boot = profile.IntentRegisterDRep
	}
	return cs, boot, true
}

// voterShare is the share of a run's DReps that vote. A run cut down from
// the chain's head count keeps the chain's voters first: they are the DReps
// that matter to governance, and at Voting of a dozen DReps a devnet would
// have no voter at all. So the run's share is the chain's voters over the
// run's members, capped at all of them; an uncut population keeps the
// fitted share.
func voterShare(pop profile.Population) float64 {
	return min(1, pop.DRep.Voting/pop.Scale())
}

// votesPerLifetime is how many votes, in expectation, a voter's vote clock
// gives each open proposal it may vote on over the proposal's lifetime at
// the least (voteFloor).
const votesPerLifetime = 3

// voteFloor is the least rate, per second, of the actor's vote clock: the
// fitted rates are per day of a chain whose proposals stay open a month
// (six five-day epochs), while a devnet's epochs are minutes long and its
// proposals close within the hour, so at the fitted rate nobody votes in
// time. The floor gives each open proposal the actor may vote on and has
// not, votesPerLifetime wakes over a lifetime, so a voter votes on about
// 1-e^-3 = 95% of them; the agenda picks which, the nearest expiry first.
// The next wake is drawn at the rate of the last one, so a voter with
// nothing to vote on still wakes as if one more proposal were open: otherwise it
// would sleep at the fitted rate, for days, through every proposal made
// meanwhile. A rate above the floor, as on a chain with real-length
// epochs, stays as fitted. Without a known lifetime there is no floor.
func (pp *Pop) voteFloor(id population.ActorID, st Subject) float64 {
	life := float64(pp.spec.Slots.EpochStart(pp.spec.GovLifetime)) / 1000
	if life <= 0 {
		return 0
	}
	g, ok := pp.env.Agenda.Agenda().(agenda.Governance)
	if !ok {
		return 0
	}
	n := 1
	for _, p := range g.OpenProposals() {
		if (p.Pools || st.Boot != profile.IntentRegisterPool) && !g.Voted(id, p.ID) {
			n++
		}
	}
	return votesPerLifetime * float64(n) / life
}

// clockRate is a clock's rate now: the vote clock at least its floor.
func (pp *Pop) clockRate(id population.ActorID, st Subject, c Clock) float64 {
	if c.Kind != profile.IntentVote {
		return c.Rate
	}
	return max(c.Rate, pp.voteFloor(id, st))
}

// proposerClocks splits the proposer's total rate over the action types in
// proportion to the fitted rates.
func proposerClocks(total float64, rates map[string]float64) []Clock {
	var sum float64
	for _, k := range slices.Sorted(maps.Keys(rates)) {
		sum += rates[k]
	}
	var cs []Clock
	for _, k := range slices.Sorted(maps.Keys(rates)) {
		if rates[k] > 0 {
			cs = append(cs, Clock{Kind: profile.IntentPropose, Action: build.ActionType(k), Rate: total * rates[k] / sum})
		}
	}
	return cs
}

// registered reports whether a population actor's registration is on the
// chain the engine follows.
func (pp *Pop) registered(id population.ActorID, boot profile.IntentKind) bool {
	if pp.env.Chain.View() == nil {
		// Genesis actors spawn before the view exists; none has registered.
		return false
	}
	switch boot {
	case profile.IntentRegisterPool:
		return pp.env.Chain.View().PoolRegistered(id)
	case profile.IntentRegisterDRep:
		return pp.env.Chain.View().DRepRegistered(id)
	}
	return pp.env.Chain.View().StakeRegistered(id)
}

// bootstrapping reports that the proposer is registered and the chain has
// no open proposal, none of its own in flight. A fitted proposal rate
// leaves governance empty for days on a fresh devnet (mainnet always has
// some open), so the voters would have nothing to vote on: the proposer
// then wakes at the boot rate and proposes at once, keeping at least one
// proposal open. Which action type it proposes is drawn from the fitted
// rates as any of its wakes, from the actor's seeded stream.
func (pp *Pop) bootstrapping(id population.ActorID, st Subject) bool {
	return st.Prof.Name == pp.spec.Proposer && len(pp.spec.Proposals) > 0 && pp.env.Chain.View() != nil &&
		pp.registered(id, st.Boot) && len(pp.env.Chain.View().OpenProposals()) == 0 && !pp.env.Flight.InFlight(id, string(profile.IntentPropose))
}

// Ready reports that a population actor wakes at its fitted rates:
// registered, and not bootstrapping governance.
func (pp *Pop) Ready(id population.ActorID, st Subject) bool {
	return pp.registered(id, st.Boot) && !pp.bootstrapping(id, st)
}

// Rate is a population actor's wake rate: the boot rate until it is
// ready, then the sum of its clocks.
func (pp *Pop) Rate(id population.ActorID, st Subject) float64 {
	if !pp.Ready(id, st) {
		return bootRate
	}
	var sum float64
	for _, c := range st.Clocks {
		sum += pp.clockRate(id, st, c)
	}

	return sum * float64(max(pp.env.Knobs.RateK(), 1))
}

// fire draws which clock woke the actor: the boot intent while it is
// unregistered, else a clock in proportion to its rate.
func (pp *Pop) fire(id population.ActorID, st Subject, r *rand.Rand) Clock {
	switch {
	case !pp.registered(id, st.Boot):
		return Clock{Kind: st.Boot}
	case len(st.Clocks) == 0, st.Booting && !pp.bootstrapping(id, st):
		// Registered since this wake was drawn: the next one is drawn at
		// the clocks' rate, which by memorylessness is the right wait.
		return Clock{Kind: agenda.Idle}
	}
	rates := make([]float64, len(st.Clocks))
	var sum float64
	for i, c := range st.Clocks {
		rates[i] = pp.clockRate(id, st, c)
		sum += rates[i]
	}

	u := r.Float64() * sum
	for i, c := range st.Clocks {
		if u < rates[i] {
			return c
		}
		u -= rates[i]
	}
	return st.Clocks[len(st.Clocks)-1]
}

// Decide is a population actor's decision: the fired clock's intent
// if its agenda scores it possible now, else idle. The proposer has no
// agenda for proposing; it proposes whenever its clock fires.
func (pp *Pop) Decide(a population.Actor, st Subject, r *rand.Rand) (agenda.Decision, build.ActionType) {
	c := pp.fire(a.ID, st, r)
	if c.Kind == agenda.Idle {
		return agenda.Decision{Intent: agenda.Intent{Kind: agenda.Idle, Actor: a.ID},
			Scores: []agenda.Score{{Kind: agenda.Idle, Value: 1, Why: "registered: waking at the fitted rates from now"}}}, ""
	}
	if pp.env.Flight.InFlight(a.ID, string(c.Kind)) {
		// A second registration beside one in flight would pay its deposit
		// twice.
		return agenda.Decision{Intent: agenda.Intent{Kind: agenda.Idle, Actor: a.ID},
			Scores: []agenda.Score{{Kind: c.Kind, Why: "already in flight"}}}, ""
	}
	if st.Prof.Name == pp.spec.Proposer {
		why := "network proposes " + string(c.Action)
		if pp.bootstrapping(a.ID, st) {
			why += ": no proposal open"
		}
		if c.Kind == profile.IntentRegisterStake {
			why = "proposal deposits return to a registered stake account"
		}
		s := agenda.Score{Kind: c.Kind, Value: 1, Why: why}
		return agenda.Decision{Intent: agenda.Intent{Kind: c.Kind, Actor: a.ID}, Scores: []agenda.Score{s}}, c.Action
	}
	var mine []agenda.Score
	for _, s := range st.Policy.Score(a, pp.env.Agenda.Agenda()) {
		if s.Kind == c.Kind {
			mine = append(mine, s)
		}
	}
	in, ss := agenda.Choose(a.ID, r, mine, nil)
	return agenda.Decision{Intent: in, Scores: ss}, c.Action
}
