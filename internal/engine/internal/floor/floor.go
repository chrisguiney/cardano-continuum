// Package floor is the engine side of the stake-floor guard
// (models/specs/stake-floor; the guard itself is internal/policy): the pool
// plan, the observed pods, the guard's input built from the chain view and
// the engine's transactions in flight, and the recovery the guard plans.
//
// The composite orders every call, so the order of emissions is the
// caller's. Floor emits PoolTier, Repair, Incident and Guarded, and nothing
// else; it submits no transaction (a recovery move is returned to the
// composite, which starts it as an intent).
//
// # Contract
//
// models/specs/stake-floor, actions:
//
//	GoDark, Retire,    Floor.Check: the one gate for every intent that can
//	Delegate           lower forging stake, whoever chose it
//	GoLive             Floor.SetTier (never guarded)
//	Fail               Floor.PodStatus
//	Repair             Floor.Recover, through repair
//	Allowed            Floor.Allows, which asks policy.Guard.Check
//
// Each rule of the STAKE-FLOOR GUARD CONTRACT is enforced in one place:
//
//	1   Floor.Check, which maps the guarded agenda kinds (guarded); the
//	    composite routes every agenda and injected GoDark through it
//	2   policy.Guard.Check: GoLive, Repair, Reregister and an adding
//	    delegation are always allowed
//	3   Floor.State: the guard's input is the chain view's schedules and
//	    registrations, the in-flight list, the plan and the pods, nothing else
//	4   chain.View.PolicyState (the schedules and the transition label) and
//	    policy.State's horizon, keyed on the label; Floor.State supplies the
//	    epoch only
//	5   Floor.inflight: every guarded transaction in flight is listed, a
//	    pool registration only as a Reregister, which policy does not count
//	6   policy.State's forging set, fed the plan (pods.Dark) and the
//	    observed pods (pods.PodRunning); Floor.Forging answers the agendas
//	7   chain.View.PolicyState: the denominator is the snapshot's own
//	8   policy.Guard, with the margin from policy.Config
//	9   internal/agenda (RetirePool offered only for a dark pool) and
//	    policy.Guard.Recover (a retiring pool is not brought live)
//	10  the engine's intent spec table (its guarded kinds take a TTL that
//	    ends inside the epoch), not here
//	11  internal/population and the engine's cohort, not here
//	12  policy.Guard.Endangered, which policy.Guard.Recover runs; the
//	    composite calls Floor.Recover after every block and pod event
//	13  Floor.Recover: GoLive and repairs at once, the moves returned in
//	    the same call for the composite to submit
//	14  Floor.Recover: a frozen ledger is a stall Incident and Stalled
package floor

import (
	"slices"

	"github.com/chrisguiney/cardano-continuum/internal/agenda"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/wire"
	"github.com/chrisguiney/cardano-continuum/internal/policy"
	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

// Stake is the chain view as the guard reads it.
type Stake interface {
	// PolicyState is the guard's state in epoch, with pods the plan and
	// pod health and inflight the engine's transactions in flight.
	PolicyState(epoch uint64, pods agenda.Pods, inflight []policy.Intent) policy.State
}

// Book is the engine's unfinished intents.
type Book interface {
	// IDs are the unfinished intents, ascending.
	IDs() []uint64
	// Get is the unfinished intent id.
	Get(id uint64) *wire.Pending
}

// Census is the population's view of the pool actors.
type Census interface {
	// Retiring reports that the pool actor's retirement has confirmed.
	Retiring(id population.ActorID) bool
}

// Journal is the decision log of the input being handled.
type Journal interface {
	Emit(at sched.Time, kind string, a population.ActorID, body any)
}

// Clock is the engine's clock.
type Clock interface {
	// Now is the time of the last tick.
	Now() sched.Time
}

// Spec is what a Floor is built from.
type Spec struct {
	Guard policy.Config
	Slots sched.Slots
	// SimulatedPods: no operator runs the pools, so a repaired pod is back
	// at once.
	SimulatedPods bool
}

// Env is what the floor shares with the rest of the engine.
type Env struct {
	Stake   Stake
	Book    Book
	Census  Census
	Journal Journal
	Clock   Clock
}

// Saved is a Floor's state in a snapshot. Every list is ascending.
type Saved struct {
	// Dark is the pool plan: pool actors the engine wants not forging.
	Dark []uint64 `json:"dark,omitempty"`
	// Down are the pool actors whose pod is observed not running.
	Down []uint64 `json:"down,omitempty"`
	// Repairs are the down pool actors a repair was asked for since.
	Repairs []uint64 `json:"repairs,omitempty"`
	// Stalled is set by a stall incident.
	Stalled bool `json:"stalled,omitzero"`
}

// pods is the pool plan and the observed pod health.
type pods struct {
	// dark is the pool plan: pools the engine wants not forging. down are
	// pools whose pod is observed not running, repairs those a repair was
	// asked for since.
	dark, down, repairs map[population.ActorID]bool
}

// Dark implements agenda.Pods: the plan wants pool not forging.
func (p *pods) Dark(pool population.ActorID) bool { return p.dark[pool] }

// PodRunning implements agenda.Pods: pool's pod is not observed down.
func (p *pods) PodRunning(pool population.ActorID) bool { return !p.down[pool] }

// Plan is the pool plan: the pool actors the engine wants dark, ascending.
func (p *pods) Plan() []population.ActorID {
	out := make([]population.ActorID, 0, len(p.dark))
	for id := range p.dark {
		out = append(out, id)
	}
	slices.Sort(out)
	return out
}

// Floor is the stake-floor guard's state and the pool plan.
type Floor struct {
	spec  Spec
	env   Env
	guard policy.Guard
	pods
	// stalled is set by a stall incident.
	stalled bool
}

// New returns a floor with every pool planned live and running.
func New(spec Spec, env Env) (*Floor, error) {
	g, err := policy.New(spec.Guard)
	if err != nil {
		return nil, err
	}
	return &Floor{
		spec: spec, env: env, guard: g,
		pods: pods{dark: map[population.ActorID]bool{}, down: map[population.ActorID]bool{}, repairs: map[population.ActorID]bool{}},
	}, nil
}

// Guard is the guard the floor asks.
func (f *Floor) Guard() policy.Guard { return f.guard }

var _ agenda.Pods = (*Floor)(nil)

// State is the guard's input now. The epoch is the clock's, or the block's
// when a block runs ahead of the last tick.
func (f *Floor) State(at sched.Time) policy.State {
	return f.env.Stake.PolicyState(f.spec.Slots.EpochAt(max(at, f.env.Clock.Now())), f, f.inflight())
}

// inflight lists the engine's guarded transactions neither settled nor
// dead, in intent order. A pool registration of a retiring pool is a
// re-registration.
func (f *Floor) inflight() []policy.Intent {
	var out []policy.Intent
	for _, id := range f.env.Book.IDs() {
		p := f.env.Book.Get(id)
		a := population.ActorID(p.Actor)
		switch profile.IntentKind(p.Kind) {
		case profile.IntentDelegate:
			out = append(out, policy.Intent{Op: policy.Delegate, Wallet: a, Pool: population.ActorID(p.To)})
		case profile.IntentUndelegate:
			out = append(out, policy.Intent{Op: policy.Undelegate, Wallet: a})
		case profile.IntentRetirePool:
			out = append(out, policy.Intent{Op: policy.Retire, Pool: a})
		case profile.IntentRegisterPool:
			if f.env.Census.Retiring(a) {
				out = append(out, policy.Intent{Op: policy.Reregister, Pool: a})
			}
		}
	}
	return out
}

// guarded is the guard's step for an agenda intent, if the intent is one
// that can lower forging stake.
func guarded(in agenda.Intent) (policy.Intent, bool) {
	switch in.Kind {
	case agenda.GoDark:
		return policy.Intent{Op: policy.GoDark, Pool: in.Actor}, true
	case agenda.GoLive:
		return policy.Intent{Op: policy.GoLive, Pool: in.Actor}, true
	case profile.IntentRetirePool:
		return policy.Intent{Op: policy.Retire, Pool: in.Actor}, true
	case profile.IntentDelegate:
		return policy.Intent{Op: policy.Delegate, Wallet: in.Actor, Pool: in.Target}, true
	case profile.IntentUndelegate:
		return policy.Intent{Op: policy.Undelegate, Wallet: in.Actor}, true
	}
	return policy.Intent{}, false
}

// Allows asks the guard about in.
func (f *Floor) Allows(at sched.Time, in agenda.Intent) policy.Verdict {
	step, ok := guarded(in)
	if !ok {
		return policy.Verdict{OK: true}
	}
	return f.guard.Check(f.State(at), step)
}

// Check asks the guard about an intent an agenda chose, recording a
// refusal.
func (f *Floor) Check(at sched.Time, in agenda.Intent) bool {
	vd := f.Allows(at, in)
	if !vd.OK {
		f.env.Journal.Emit(at, wire.KindGuarded, in.Actor, wire.Guarded{Kind: string(in.Kind), Target: uint64(in.Target), Why: vd.Why})
	}
	return vd.OK
}

// Forging reports that pool counts as forging to the guard now: registered,
// planned live, pod running, and no retirement in flight.
func (f *Floor) Forging(pool population.ActorID) bool {
	s := f.State(f.env.Clock.Now())
	for _, in := range s.InFlight {
		if in.Op == policy.Retire && in.Pool == pool {
			return false
		}
	}
	for _, p := range s.Pools {
		if p.ID == pool {
			return p.Reg == policy.Registered && p.Live && p.Running
		}
	}
	return false
}

// SetTier records the plan for a pool: dark or live. Live runs patch the
// ContinuumPool from it; the fake simulates it.
func (f *Floor) SetTier(at sched.Time, pool population.ActorID, dark bool, why string) {
	if f.dark[pool] == dark {
		return
	}
	if dark {
		f.dark[pool] = true
	} else {
		delete(f.dark, pool)
	}
	f.env.Journal.Emit(at, wire.KindPoolTier, pool, wire.PoolTier{Dark: dark, Why: why})
}

// Take takes an operator's go-dark or go-live, which needs no transaction,
// after the guard allows it.
func (f *Floor) Take(at sched.Time, in agenda.Intent) {
	f.SetTier(at, in.Actor, in.Kind == agenda.GoDark, "agenda")
}

// PodStatus applies an observed pod state of a pool actor.
func (f *Floor) PodStatus(id population.ActorID, running bool) {
	if running {
		delete(f.down, id)
		delete(f.repairs, id)
	} else {
		f.down[id] = true
	}
}

// Recover runs the guard's recovery for the state at at: GoLive and repairs
// at once, and returns a move for each stranded wallet, for the caller to
// submit in order. A frozen ledger is a stall incident instead.
func (f *Floor) Recover(at sched.Time) []policy.Intent {
	s := f.State(at)
	if f.stalled || len(s.Pools) == 0 {
		return nil
	}
	if s.Frozen() {
		f.stalled = true
		f.env.Journal.Emit(at, wire.KindIncident, 0, wire.Incident{Kind: "stall", Epoch: s.Epoch, Label: s.Label})
		return nil
	}
	plan := f.guard.Recover(s)
	for _, pool := range plan.GoLive {
		f.SetTier(at, pool, false, "recovery")
	}
	for _, pool := range plan.Repair {
		f.repair(at, pool)
	}
	return plan.Moves
}

// repair asks for a dead pod to be brought back, once per death. With
// simulated pods the pod is back at once.
func (f *Floor) repair(at sched.Time, pool population.ActorID) {
	if f.repairs[pool] {
		return
	}
	f.env.Journal.Emit(at, wire.KindRepair, pool, wire.Repair{})
	if f.spec.SimulatedPods {
		delete(f.down, pool)
		return
	}
	f.repairs[pool] = true
}

// Stalled reports a stall incident: the run must stop.
func (f *Floor) Stalled() bool { return f.stalled }

// Save is the floor's state for a snapshot.
func (f *Floor) Save() Saved {
	return Saved{Dark: idSet(f.dark), Down: idSet(f.down), Repairs: idSet(f.repairs), Stalled: f.stalled}
}

// Load replaces the floor's state with s.
func (f *Floor) Load(s Saved) {
	f.dark, f.down, f.repairs = setOf(s.Dark), setOf(s.Down), setOf(s.Repairs)
	f.stalled = s.Stalled
}

func idSet(m map[population.ActorID]bool) []uint64 {
	out := make([]uint64, 0, len(m))
	for id := range m {
		out = append(out, uint64(id))
	}
	slices.Sort(out)
	return out
}

func setOf(ids []uint64) map[population.ActorID]bool {
	m := make(map[population.ActorID]bool, len(ids))
	for _, id := range ids {
		m[population.ActorID(id)] = true
	}
	return m
}
