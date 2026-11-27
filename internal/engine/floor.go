package engine

import (
	"context"
	"errors"
	"slices"

	"github.com/chrisguiney/cardano-continuum/internal/agenda"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/wire"
	"github.com/chrisguiney/cardano-continuum/internal/policy"
	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

// This file is the engine side of the stake-floor guard
// (models/specs/stake-floor, internal/policy): stakeFloor holds the pool
// plan, the observed pods and the guard's input, and the engine routes into
// it pod observations, the recovery run after every block and pod
// event, and the recovery moves, which are the engine's own intents.

// ErrStalled: an epoch passed with no block. The ledger never runs another
// epoch transition after that, so the run stops (contract rule 14).
var ErrStalled = errors.New("engine: an epoch passed with no block")

// floorHooks is the rest of the engine as the stake floor reaches it.
type floorHooks struct{ e *Engine }

// PolicyState implements floor.Stake.
func (h floorHooks) PolicyState(epoch uint64, pods agenda.Pods, inflight []policy.Intent) policy.State {
	return h.e.win.View().PolicyState(epoch, pods, inflight)
}

// Retiring implements floor.Census.
func (h floorHooks) Retiring(id population.ActorID) bool {
	act, _ := h.e.co.Get(id)
	return act.State == population.Retiring
}

// poolStatus applies an observed pod state, then recovers.
func (e *Engine) poolStatus(ctx context.Context, at sched.Time, s PoolStatus) error {
	id := population.ActorID(s.Pool)
	if _, ok := e.co.Ring.PoolOf(id); !ok {
		return nil
	}
	e.fl.PodStatus(id, s.Running)
	return e.recover(ctx, at)
}

// recover runs the guard's recovery for the state at at and starts it:
// the floor takes GoLive and repairs at once, then each stranded wallet's
// move starts here, in the plan's order.
func (e *Engine) recover(ctx context.Context, at sched.Time) error {
	for _, mv := range e.fl.Recover(at) {
		if err := e.move1(ctx, at, mv); err != nil {
			return err
		}
	}
	return nil
}

// move1 starts a recovery move: the engine's own delegation of a stranded
// wallet onto a forging pool. It needs no agenda and is never guarded.
func (e *Engine) move1(ctx context.Context, at sched.Time, mv policy.Intent) error {
	a, ok := e.co.Get(mv.Wallet)
	if !ok {
		return nil
	}
	p := &wire.Pending{Actor: uint64(a.ID), Args: wire.Args{Kind: string(profile.IntentDelegate), To: uint64(mv.Pool), Register: !e.win.View().StakeRegistered(a.ID)}}
	why, err := e.start(ctx, at, p)
	if err != nil {
		return err
	}
	if why != "" {
		e.jr.Emit(at, KindSkipped, a.ID, Skipped{Kind: "recovery_move", Why: why})
	}
	return nil
}

// Stalled reports a stall incident: the run must stop.
func (e *Engine) Stalled() bool { return e.fl.Stalled() }

// Plan is the engine's pool plan: the pool actors it wants dark, ascending.
func (e *Engine) Plan() []population.ActorID { return e.fl.Plan() }

// floorStake is the chain view as the guard reads it.
type floorStake interface {
	// PolicyState is the guard's state in epoch, with floorPods the plan and
	// pod health and inflight the engine's transactions in flight.
	PolicyState(epoch uint64, floorPods agenda.Pods, inflight []policy.Intent) policy.State
}

// floorBook is the engine's unfinished intents.
type floorBook interface {
	// IDs are the unfinished intents, ascending.
	IDs() []uint64
	// Get is the unfinished intent id.
	Get(id uint64) *wire.Pending
}

// floorCensus is the population's view of the pool actors.
type floorCensus interface {
	// Retiring reports that the pool actor's retirement has confirmed.
	Retiring(id population.ActorID) bool
}

// floorJournal is the decision log of the input being handled.
type floorJournal interface {
	Emit(at sched.Time, kind string, a population.ActorID, body any)
}

// floorClock is the engine's clock.
type floorClock interface {
	// Now is the time of the last tick.
	Now() sched.Time
}

// floorSpec is what a stakeFloor is built from.
type floorSpec struct {
	Guard policy.Config
	Slots sched.Slots
	// SimulatedPods: no operator runs the pools, so a repaired pod is back
	// at once.
	SimulatedPods bool
}

// floorEnv is what the floor shares with the rest of the engine.
type floorEnv struct {
	floorStake   floorStake
	floorBook    floorBook
	floorCensus  floorCensus
	floorJournal floorJournal
	floorClock   floorClock
}

// floorSaved is a stakeFloor's state in a snapshot. Every list is ascending.
type floorSaved struct {
	Dark, Down, Repairs []uint64
	Stalled             bool
}

// floorPods is the pool plan and the observed pod health.
type floorPods struct {
	// dark is the pool plan: pools the engine wants not forging. down are
	// pools whose pod is observed not running, repairs those a repair was
	// asked for since.
	dark, down, repairs map[population.ActorID]bool
}

// Dark implements agenda.Pods: the plan wants pool not forging.
func (p *floorPods) Dark(pool population.ActorID) bool { return p.dark[pool] }

// PodRunning implements agenda.Pods: pool's pod is not observed down.
func (p *floorPods) PodRunning(pool population.ActorID) bool { return !p.down[pool] }

// Plan is the pool plan: the pool actors the engine wants dark, ascending.
func (p *floorPods) Plan() []population.ActorID {
	out := make([]population.ActorID, 0, len(p.dark))
	for id := range p.dark {
		out = append(out, id)
	}
	slices.Sort(out)
	return out
}

// stakeFloor is the stake-floor guard's state and the pool plan.
type stakeFloor struct {
	spec  floorSpec
	env   floorEnv
	guard policy.Guard
	floorPods
	// stalled is set by a stall incident.
	stalled bool
}

// newFloor returns a floor with every pool planned live and running.
func newFloor(spec floorSpec, env floorEnv) (*stakeFloor, error) {
	g, err := policy.New(spec.Guard)
	if err != nil {
		return nil, err
	}
	return &stakeFloor{
		spec: spec, env: env, guard: g,
		floorPods: floorPods{dark: map[population.ActorID]bool{}, down: map[population.ActorID]bool{}, repairs: map[population.ActorID]bool{}},
	}, nil
}

// Guard is the guard the floor asks.
func (f *stakeFloor) Guard() policy.Guard { return f.guard }

var _ agenda.Pods = (*stakeFloor)(nil)

// State is the guard's input now. The epoch is the clock's, or the block's
// when a block runs ahead of the last tick.
func (f *stakeFloor) State(at sched.Time) policy.State {
	return f.env.floorStake.PolicyState(f.spec.Slots.EpochAt(max(at, f.env.floorClock.Now())), f, f.inflight())
}

// inflight lists the engine's guarded transactions neither settled nor
// dead, in intent order. A pool registration of a retiring pool is a
// re-registration.
func (f *stakeFloor) inflight() []policy.Intent {
	var out []policy.Intent
	for _, id := range f.env.floorBook.IDs() {
		p := f.env.floorBook.Get(id)
		a := population.ActorID(p.Actor)
		switch profile.IntentKind(p.Kind) {
		case profile.IntentDelegate:
			out = append(out, policy.Intent{Op: policy.Delegate, Wallet: a, Pool: population.ActorID(p.To)})
		case profile.IntentUndelegate:
			out = append(out, policy.Intent{Op: policy.Undelegate, Wallet: a})
		case profile.IntentRetirePool:
			out = append(out, policy.Intent{Op: policy.Retire, Pool: a})
		case profile.IntentRegisterPool:
			if f.env.floorCensus.Retiring(a) {
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
func (f *stakeFloor) Allows(at sched.Time, in agenda.Intent) policy.Verdict {
	step, ok := guarded(in)
	if !ok {
		return policy.Verdict{OK: true}
	}
	return f.guard.Check(f.State(at), step)
}

// Check asks the guard about an intent an agenda chose, recording a
// refusal.
func (f *stakeFloor) Check(at sched.Time, in agenda.Intent) bool {
	vd := f.Allows(at, in)
	if !vd.OK {
		f.env.floorJournal.Emit(at, wire.KindGuarded, in.Actor, wire.Guarded{Kind: string(in.Kind), Target: uint64(in.Target), Why: vd.Why})
	}
	return vd.OK
}

// Forging reports that pool counts as forging to the guard now: registered,
// planned live, pod running, and no retirement in flight.
func (f *stakeFloor) Forging(pool population.ActorID) bool {
	s := f.State(f.env.floorClock.Now())
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
func (f *stakeFloor) SetTier(at sched.Time, pool population.ActorID, dark bool, why string) {
	if f.dark[pool] == dark {
		return
	}
	if dark {
		f.dark[pool] = true
	} else {
		delete(f.dark, pool)
	}
	f.env.floorJournal.Emit(at, wire.KindPoolTier, pool, wire.PoolTier{Dark: dark, Why: why})
}

// Take takes an operator's go-dark or go-live, which needs no transaction,
// after the guard allows it.
func (f *stakeFloor) Take(at sched.Time, in agenda.Intent) {
	f.SetTier(at, in.Actor, in.Kind == agenda.GoDark, "agenda")
}

// PodStatus applies an observed pod state of a pool actor.
func (f *stakeFloor) PodStatus(id population.ActorID, running bool) {
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
func (f *stakeFloor) Recover(at sched.Time) []policy.Intent {
	s := f.State(at)
	if f.stalled || len(s.Pools) == 0 {
		return nil
	}
	if s.Frozen() {
		f.stalled = true
		f.env.floorJournal.Emit(at, wire.KindIncident, 0, wire.Incident{Kind: "stall", Epoch: s.Epoch, Label: s.Label})
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
// simulated floorPods the pod is back at once.
func (f *stakeFloor) repair(at sched.Time, pool population.ActorID) {
	if f.repairs[pool] {
		return
	}
	f.env.floorJournal.Emit(at, wire.KindRepair, pool, wire.Repair{})
	if f.spec.SimulatedPods {
		delete(f.down, pool)
		return
	}
	f.repairs[pool] = true
}

// Stalled reports a stall incident: the run must stop.
func (f *stakeFloor) Stalled() bool { return f.stalled }

// Save is the floor's state for a snapshot.
func (f *stakeFloor) Save() floorSaved {
	return floorSaved{Dark: idSet(f.dark), Down: idSet(f.down), Repairs: idSet(f.repairs), Stalled: f.stalled}
}

// Load replaces the floor's state with s.
func (f *stakeFloor) Load(s floorSaved) {
	f.dark, f.down, f.repairs = floorSet(s.Dark), floorSet(s.Down), floorSet(s.Repairs)
	f.stalled = s.Stalled
}

func floorSet(ids []uint64) map[population.ActorID]bool {
	m := make(map[population.ActorID]bool, len(ids))
	for _, id := range ids {
		m[population.ActorID(id)] = true
	}
	return m
}
