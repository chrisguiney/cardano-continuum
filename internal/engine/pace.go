package engine

import (
	"maps"
	"math"
	"math/rand/v2"
	"slices"

	"github.com/chrisguiney/cardano-continuum/internal/agenda"
	"github.com/chrisguiney/cardano-continuum/internal/build"
	evclock "github.com/chrisguiney/cardano-continuum/internal/engine/internal/clock"
	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

// The market signal is a Hawkes process over the engine's own submissions,
// with an exponential kernel: intensity = mu + excess, where each
// submission adds alpha to excess and excess decays at rate beta. Only the
// excess and when it was last updated are state, so the snapshot carries
// two numbers instead of the event history sched.Hawkes keeps.
const (
	marketMu    = 0.1 // submissions per second with no excitation
	marketAlpha = 0.5
	marketBeta  = 1.0
)

// marketBaseline is the process's long-run mean rate, mu / (1 - alpha/beta):
// Burst is 1 when submissions run at it.
const marketBaseline = marketMu / (1 - marketAlpha/marketBeta)

type market struct {
	Excess float64    `cbor:"1,keyasint,omitempty"`
	At     sched.Time `cbor:"2,keyasint,omitempty"`
}

// decayed is the excess at now; times before the last update read as it.
func (m market) decayed(now sched.Time) float64 {
	dt := max(0, float64(now-m.At)/1000)
	return m.Excess * math.Exp(-marketBeta*dt)
}

// excite records a submission at at.
func (m *market) excite(at sched.Time) {
	m.Excess = m.decayed(at) + marketAlpha
	m.At = max(m.At, at)
}

// burst is the intensity at now over the long-run baseline.
func (m market) burst(now sched.Time) float64 {
	return (marketMu + m.decayed(now)) / marketBaseline
}

// pace decides when an actor next acts: its base rate, the diurnal and
// Hawkes shaping, a population actor's intent clocks, and the market
// signal that scales the agenda's reading of the chain's busyness.
type pace struct {
	e      *Engine
	market market
}

// maxFollowWaits bounds how many blocks a follow-up waits to act.
const maxFollowWaits = 30

// wait defers a follow-up that found nothing to do one block, and reports
// whether it did. Nothing to do yet is most often because the actor's coins
// are in the attempt this follows: the planner offers only settled outputs,
// so the change takes K blocks. The follow-up waits a block at a time
// instead of being lost, or a burst could never be more than one
// transaction.
func (pc *pace) wait(a population.Actor, st *actor, at sched.Time, d agenda.Decision) bool {
	if !st.FollowUp || d.Intent.Kind != agenda.Idle || st.Waits >= maxFollowWaits {
		return false
	}
	st.Waits++
	st.ActAt = at + pc.blockMs()
	st.ActH = pc.e.clk.Push(st.ActAt, evclock.Event{Kind: evclock.Act, Actor: a.ID})
	return true
}

// followWeights are the weights of a follow-up's decision: a burst is
// follow-up activity, so a follow-up does something. A burst of idle
// wake-ups would leave transactions as spread out as without it.
func (pc *pace) followWeights(p profile.Profile) map[profile.IntentKind]float64 {
	w := maps.Clone(pc.weightsOf(p))
	if w == nil {
		w = map[profile.IntentKind]float64{}
	}
	w[agenda.Idle] = 0
	return w
}

// owe settles a follow-up's debt after its decision and adds the children
// of a decision that did something. It draws from r after the decision's
// own draws, in that order.
func (pc *pace) owe(st *actor, d agenda.Decision, r *rand.Rand) {
	st.Waits = 0
	if st.FollowUp {
		st.Owed--
	}
	if h := st.Prof.Rates.Hawkes; h.Alpha > 0 && d.Intent.Kind != agenda.Idle {
		st.Owed += uint32(sched.Branching{Ratio: h.Alpha / h.Beta}.Children(r))
	}
}

// clocks are a population actor's intent clocks, in kind order, and the
// intent it registers with; ok is false for an actor of no population.
func (pc *pace) clocks(a population.Actor, p profile.Profile) (cs []clock, boot profile.IntentKind, ok bool) {
	if p.Name == proposerName && len(pc.e.cfg.Network.Proposals) > 0 {
		return proposerClocks(p.Rates.Base[profile.IntentPropose], pc.e.cfg.Network.Proposals), profile.IntentRegisterStake, true
	}
	pop, found := pc.e.cfg.population(p.Name)
	if !found || pop.Count == 0 {
		return nil, "", false
	}
	for _, k := range slices.Sorted(maps.Keys(p.Rates.Base)) {
		c := clock{Kind: k, Rate: p.Rates.Base[k]}
		if k == profile.IntentVote && pop.DRep != nil {
			// Only Voting of the DReps vote at all, so each voter votes at
			// the population's rate over that share.
			if pop.DRep.Voting <= 0 || a.Stream("voter").Float64() >= pop.DRep.Voting {
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

// proposerClocks splits the proposer's total rate over the action types in
// proportion to the fitted rates.
func proposerClocks(total float64, rates map[string]float64) []clock {
	var sum float64
	for _, k := range sortedKeys(rates) {
		sum += rates[k]
	}
	var cs []clock
	for _, k := range sortedKeys(rates) {
		if rates[k] > 0 {
			cs = append(cs, clock{Kind: profile.IntentPropose, Action: build.ActionType(k), Rate: total * rates[k] / sum})
		}
	}
	return cs
}

// registered reports whether a population actor's registration is on the
// chain the engine follows.
func (pc *pace) registered(id population.ActorID, boot profile.IntentKind) bool {
	if pc.e.win.View() == nil {
		// Genesis actors spawn before the view exists; none has registered.
		return false
	}
	switch boot {
	case profile.IntentRegisterPool:
		return pc.e.win.View().PoolRegistered(id)
	case profile.IntentRegisterDRep:
		return pc.e.win.View().DRepRegistered(id)
	}
	return pc.e.win.View().StakeRegistered(id)
}

// bootstrapping reports that the proposer is registered and the chain has
// no open proposal, none of its own in flight. A fitted proposal rate
// leaves governance empty for days on a fresh devnet (mainnet always has
// some open), so the voters would have nothing to vote on: the proposer
// then wakes at the boot rate and proposes at once, keeping at least one
// proposal open. Which action type it proposes is drawn from the fitted
// rates as any of its wakes, from the actor's seeded stream.
func (pc *pace) bootstrapping(id population.ActorID, st *actor) bool {
	return st.Prof.Name == proposerName && len(pc.e.cfg.Network.Proposals) > 0 && pc.e.win.View() != nil &&
		pc.registered(id, st.Boot) && len(pc.e.win.View().OpenProposals()) == 0 && !pc.e.inFlight(id, string(profile.IntentPropose))
}

// ready reports that a population actor wakes at its fitted rates:
// registered, and not bootstrapping governance.
func (pc *pace) ready(id population.ActorID, st *actor) bool {
	return pc.registered(id, st.Boot) && !pc.bootstrapping(id, st)
}

// clockRate is a population actor's wake rate: the boot rate until it is
// ready, then the sum of its clocks.
func (pc *pace) clockRate(id population.ActorID, st *actor) float64 {
	if !pc.ready(id, st) {
		return bootRate
	}
	var sum float64
	for _, c := range st.Clocks {
		sum += c.Rate
	}
	return sum * float64(max(pc.e.rateK, 1))
}

// fire draws which clock woke the actor: the boot intent while it is
// unregistered, else a clock in proportion to its rate.
func (pc *pace) fire(id population.ActorID, st *actor, r *rand.Rand) clock {
	switch {
	case !pc.registered(id, st.Boot):
		return clock{Kind: st.Boot}
	case len(st.Clocks) == 0, st.Booting && !pc.bootstrapping(id, st):
		// Registered since this wake was drawn: the next one is drawn at
		// the clocks' rate, which by memorylessness is the right wait.
		return clock{Kind: agenda.Idle}
	}
	var sum float64
	for _, c := range st.Clocks {
		sum += c.Rate
	}
	u := r.Float64() * sum
	for _, c := range st.Clocks {
		if u < c.Rate {
			return c
		}
		u -= c.Rate
	}
	return st.Clocks[len(st.Clocks)-1]
}

// decideClock is a population actor's decision: the fired clock's intent
// if its agenda scores it possible now, else idle. The proposer has no
// agenda for proposing; it proposes whenever its clock fires.
func (pc *pace) decideClock(a population.Actor, st *actor, r *rand.Rand) (agenda.Decision, build.ActionType) {
	c := pc.fire(a.ID, st, r)
	if c.Kind == agenda.Idle {
		return agenda.Decision{Intent: agenda.Intent{Kind: agenda.Idle, Actor: a.ID},
			Scores: []agenda.Score{{Kind: agenda.Idle, Value: 1, Why: "registered: waking at the fitted rates from now"}}}, ""
	}
	if pc.e.inFlight(a.ID, string(c.Kind)) {
		// A second registration beside one in flight would pay its deposit
		// twice.
		return agenda.Decision{Intent: agenda.Intent{Kind: agenda.Idle, Actor: a.ID},
			Scores: []agenda.Score{{Kind: c.Kind, Why: "already in flight"}}}, ""
	}
	if st.Prof.Name == proposerName {
		why := "network proposes " + string(c.Action)
		if pc.bootstrapping(a.ID, st) {
			why += ": no proposal open"
		}
		if c.Kind == profile.IntentRegisterStake {
			why = "proposal deposits return to a registered stake account"
		}
		s := agenda.Score{Kind: c.Kind, Value: 1, Why: why}
		return agenda.Decision{Intent: agenda.Intent{Kind: c.Kind, Actor: a.ID}, Scores: []agenda.Score{s}}, c.Action
	}
	var mine []agenda.Score
	for _, s := range st.Policy.Score(a, view{e: pc.e}) {
		if s.Kind == c.Kind {
			mine = append(mine, s)
		}
	}
	in, ss := agenda.Choose(a.ID, r, mine, nil)
	return agenda.Decision{Intent: in, Scores: ss}, c.Action
}

// nextClock schedules a population actor's next wake. A retired pool or
// DRep never wakes again.
func (pc *pace) nextClock(a population.Actor, st *actor, now sched.Time) {
	st.ActAt = population.Never
	if a.State == population.Retired || a.State == population.Expired {
		return
	}
	st.Booting = !pc.ready(a.ID, st)
	if at := sched.NextArrival(st.Draw(a, "arrival"), pc.clockRate(a.ID, st), nil, now); at != population.Never {
		st.ActAt = at
		st.ActH = pc.e.clk.Push(at, evclock.Event{Kind: evclock.Act, Actor: a.ID})
	}
}

func (pc *pace) nextAct(a population.Actor, st *actor, now sched.Time) {
	if st.Boot != "" {
		pc.nextClock(a, st, now)
		return
	}
	rate := st.Rate * a.Traits.RateMult * float64(max(pc.e.rateK, 1))
	if rate <= 0 || st.Policy == nil {
		st.ActAt = population.Never
		return
	}
	var mod sched.Modulation
	if d := st.Prof.Rates.Diurnal; d > 0 {
		mod = sched.Daily{Amplitude: d}
	}
	if h := st.Prof.Rates.Hawkes; h.Alpha > 0 {
		// The Hawkes process in its branching form: owed follow-ups come
		// one at a time, each a decay wait after the last act, racing the
		// base-rate arrivals (immigrants), which Poisson's memorylessness
		// lets the race redraw. The base rate is cut so the clusters
		// average rate: mean = mu / (1 - alpha/beta).
		b := sched.Branching{Ratio: h.Alpha / h.Beta, Beta: h.Beta}
		r := st.Draw(a, "arrival")
		st.ActAt, st.FollowUp = sched.NextArrival(r, rate*(1-b.Ratio), mod, now), false
		if st.Owed > 0 {
			if at := now + b.Delay(r); at < st.ActAt {
				st.ActAt, st.FollowUp = at, true
			}
		}
		st.ActH = pc.e.clk.Push(st.ActAt, evclock.Event{Kind: evclock.Act, Actor: a.ID})
		return
	}
	st.ActAt = sched.NextArrival(st.Draw(a, "arrival"), rate, mod, now)
	st.ActH = pc.e.clk.Push(st.ActAt, evclock.Event{Kind: evclock.Act, Actor: a.ID})
}

// blockMs is the fake chain's block interval in milliseconds, or 20 slots
// when the chain's interval is unknown (a live node).
func (pc *pace) blockMs() sched.Time {
	n := pc.e.cfg.BlockEvery
	if n == 0 {
		n = 20
	}
	return max(pc.e.cfg.Slots.SlotStart(n)-pc.e.cfg.Slots.SlotStart(0), 1)
}

// weightsOf is a profile's intent weights with the controls' overrides.
func (pc *pace) weightsOf(p profile.Profile) map[profile.IntentKind]float64 {
	if len(pc.e.weights) == 0 {
		return p.Weights
	}
	w := maps.Clone(p.Weights)
	if w == nil {
		w = map[profile.IntentKind]float64{}
	}
	maps.Copy(w, pc.e.weights)
	return w
}
