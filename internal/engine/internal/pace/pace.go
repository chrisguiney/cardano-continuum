// Package pace decides when an actor next acts: its base rate with the
// diurnal and Hawkes shaping, a population actor's per-intent clocks (Pop,
// promoted here), and the market signal the agenda reads as Burst.
//
//	Pace    next act, Hawkes follow-ups, the market
//	Pop     population clocks: boot, fitted rates, which clock fired
//
// The composite orders every call. Each wake draws from the actor's own
// seeded stream through Subject, so the order of draws is the caller's
// order of calls, and the market is the only state a Pace owns.
package pace

import (
	"maps"
	"math/rand/v2"

	"github.com/chrisguiney/cardano-continuum/internal/agenda"
	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

// Scheduler is the event queue as the pace uses it.
type Scheduler interface {
	// Act schedules an actor's act event.
	Act(at sched.Time, id population.ActorID) sched.Handle
}

// Knobs are the operator's controls the pace reads.
type Knobs interface {
	// RateK is the rate multiplier; less than one reads as one.
	RateK() uint32
	// Weights are the intent weights the controls override, by kind.
	Weights() map[profile.IntentKind]float64
}

// Spec is what a Pace is built from.
type Spec struct {
	Proposer  string
	Proposals map[string]float64
	// Population is the file's population called name.
	Population func(name string) (profile.Population, bool)
	Slots      sched.Slots
	// BlockEvery is the fake chain's block interval in slots, zero when
	// the chain's interval is unknown (a live node).
	BlockEvery uint64
	// GovLifetime is govActionLifetime in epochs, zero when unknown: how
	// long a proposal stays open for votes, which sets the vote clocks'
	// floor (Pop.voteFloor).
	GovLifetime uint64
	// Committee is the number of constitutional committee seats the
	// proposer votes for; zero gives it no committee clock.
	Committee int
}

// Env is what the pace shares with the rest of the engine.
type Env struct {
	Sched  Scheduler
	Knobs  Knobs
	Chain  Chain
	Flight Flight
	Agenda Agenda
}

// maxFollowWaits bounds how many blocks a follow-up waits to act.
const maxFollowWaits = 30

// Pace is when actors act.
type Pace struct {
	*Pop
	spec   Spec
	env    Env
	market Market
}

// New returns a pace with no market excitation.
func New(spec Spec, env Env) *Pace {
	return &Pace{Pop: newPop(spec, env), spec: spec, env: env}
}

// Save is the market signal's state, the pacing's snapshot section.
func (pc *Pace) Save() Market { return pc.market }

// Load restores the market signal's state.
func (pc *Pace) Load(m Market) { pc.market = m }

// Excite records a submission at at.
func (pc *Pace) Excite(at sched.Time) { pc.market.excite(at) }

// Burst is the submission intensity at now over its long-run baseline.
func (pc *Pace) Burst(now sched.Time) float64 { return pc.market.burst(now) }

// Wait defers a follow-up that found nothing to do one block, and reports
// whether it did. Nothing to do yet is most often because the actor's coins
// are in the attempt this follows: the planner offers only settled outputs,
// so the change takes K blocks. The follow-up waits a block at a time
// instead of being lost, or a burst could never be more than one
// transaction.
func (pc *Pace) Wait(a population.Actor, st Subject, at sched.Time, d agenda.Decision) bool {
	if !st.FollowUp || d.Intent.Kind != agenda.Idle || st.Waits >= maxFollowWaits {
		return false
	}
	st.Waits++
	st.ActAt = at + pc.Block()
	st.ActH = pc.env.Sched.Act(st.ActAt, a.ID)
	return true
}

// FollowWeights are the weights of a follow-up's decision: a burst is
// follow-up activity, so a follow-up does something. A burst of idle
// wake-ups would leave transactions as spread out as without it.
func (pc *Pace) FollowWeights(p profile.Profile) map[profile.IntentKind]float64 {
	w := maps.Clone(pc.Weights(p))
	if w == nil {
		w = map[profile.IntentKind]float64{}
	}
	w[agenda.Idle] = 0
	return w
}

// Owe settles a follow-up's debt after its decision and adds the children
// of a decision that did something. It draws from r after the decision's
// own draws, in that order.
func (pc *Pace) Owe(st Subject, d agenda.Decision, r *rand.Rand) {
	st.Waits = 0
	if st.FollowUp {
		st.Owed--
	}
	if h := st.Prof.Rates.Hawkes; h.Alpha > 0 && d.Intent.Kind != agenda.Idle {
		st.Owed += uint32(sched.Branching{Ratio: h.Alpha / h.Beta}.Children(r))
	}
}

// Block is the fake chain's block interval in milliseconds, or 20 slots
// when the chain's interval is unknown (a live node).
func (pc *Pace) Block() sched.Time {
	n := pc.spec.BlockEvery
	if n == 0 {
		n = 20
	}
	return max(pc.spec.Slots.SlotStart(n)-pc.spec.Slots.SlotStart(0), 1)
}

// Weights is a profile's intent weights with the controls' overrides.
func (pc *Pace) Weights(p profile.Profile) map[profile.IntentKind]float64 {
	over := pc.env.Knobs.Weights()
	if len(over) == 0 {
		return p.Weights
	}
	w := maps.Clone(p.Weights)
	if w == nil {
		w = map[profile.IntentKind]float64{}
	}
	maps.Copy(w, over)
	return w
}

// nextClock schedules a population actor's next wake. A retired pool or
// DRep never wakes again.
func (pc *Pace) nextClock(a population.Actor, st Subject, now sched.Time) {
	st.ActAt = population.Never
	if a.State == population.Retired || a.State == population.Expired {
		return
	}
	st.Booting = !pc.Ready(a.ID, st)
	if at := sched.NextArrival(st.Draw(a, "arrival"), pc.Rate(a.ID, st), nil, now); at != population.Never {
		st.ActAt = at
		st.ActH = pc.env.Sched.Act(at, a.ID)
	}
}

// Next schedules the actor's next act.
func (pc *Pace) Next(a population.Actor, st Subject, now sched.Time) {
	if st.Boot != "" {
		pc.nextClock(a, st, now)
		return
	}
	rate := st.Rate * a.Traits.RateMult * float64(max(pc.env.Knobs.RateK(), 1))
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
		st.ActH = pc.env.Sched.Act(st.ActAt, a.ID)
		return
	}
	st.ActAt = sched.NextArrival(st.Draw(a, "arrival"), rate, mod, now)
	st.ActH = pc.env.Sched.Act(st.ActAt, a.ID)
}
