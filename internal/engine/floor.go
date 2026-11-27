package engine

import (
	"context"
	"errors"
	"slices"

	"github.com/chrisguiney/cardano-continuum/internal/agenda"
	"github.com/chrisguiney/cardano-continuum/internal/policy"
	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

// This file is the engine side of the stake-floor guard
// (models/specs/stake-floor, internal/policy): it builds the guard's State
// from the chain view, the pool plan, the observed pods and the engine's
// own transactions in flight; it routes every step that can lower forging
// stake through Check, whoever chose it (contract rule 1); and after every
// block and pod event it runs Recover and submits the plan within the epoch.

// ErrStalled: an epoch passed with no block. The ledger never runs another
// epoch transition after that, so the run stops (contract rule 14).
var ErrStalled = errors.New("engine: an epoch passed with no block")

// defaultFloor fills the fields of Config.Floor left zero.
var defaultFloor = policy.Config{Floor: 50, Margin: 1}

func (c *Config) floor() policy.Config {
	f := c.Floor
	if f.Floor == 0 {
		f.Floor = defaultFloor.Floor
	}
	if f.Margin == 0 {
		f.Margin = defaultFloor.Margin
	}
	return f
}

// podsView is the plan and the observed pod health, as agenda.Pods.
type podsView struct{ e *Engine }

func (p podsView) Dark(pool population.ActorID) bool       { return p.e.dark[pool] }
func (p podsView) PodRunning(pool population.ActorID) bool { return !p.e.down[pool] }

// floorState is the guard's input now. The epoch is the clock's, or the
// block's when a block runs ahead of the last tick.
func (e *Engine) floorState(at sched.Time) policy.State {
	return e.win.View().PolicyState(e.cfg.Slots.EpochAt(max(at, e.clk.Now())), podsView{e}, e.inflight())
}

// inflight lists the engine's guarded transactions neither settled nor
// dead, in intent order. A pool registration of a retiring pool is a
// re-registration.
func (e *Engine) inflight() []policy.Intent {
	var out []policy.Intent
	for _, id := range e.pay.IDs() {
		p := e.pay.Get(id)
		a := population.ActorID(p.Actor)
		switch profile.IntentKind(p.Kind) {
		case profile.IntentDelegate:
			out = append(out, policy.Intent{Op: policy.Delegate, Wallet: a, Pool: population.ActorID(p.To)})
		case profile.IntentUndelegate:
			out = append(out, policy.Intent{Op: policy.Undelegate, Wallet: a})
		case profile.IntentRetirePool:
			out = append(out, policy.Intent{Op: policy.Retire, Pool: a})
		case profile.IntentRegisterPool:
			if act, _ := e.co.Get(a); act.State == population.Retiring {
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

// allows asks the guard about in.
func (e *Engine) allows(at sched.Time, in agenda.Intent) policy.Verdict {
	step, ok := guarded(in)
	if !ok {
		return policy.Verdict{OK: true}
	}
	return e.guard.Check(e.floorState(at), step)
}

// Forging implements agenda.Floor.
func (v view) Forging(pool population.ActorID) bool {
	s := v.e.floorState(v.e.clk.Now())
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

// Allows implements agenda.Floor.
func (v view) Allows(in agenda.Intent) (bool, string) {
	vd := v.e.allows(v.e.clk.Now(), in)
	return vd.OK, vd.Why
}

// Dark implements agenda.Pods.
func (v view) Dark(pool population.ActorID) bool { return podsView(v).Dark(pool) }

// PodRunning implements agenda.Pods.
func (v view) PodRunning(pool population.ActorID) bool { return podsView(v).PodRunning(pool) }

var (
	_ agenda.Floor = view{}
	_ agenda.Pods  = view{}
)

// setTier records the plan for a pool: dark or live. Live runs patch the
// ContinuumPool from it; the fake simulates it.
func (e *Engine) setTier(at sched.Time, pool population.ActorID, dark bool, why string) {
	if e.dark[pool] == dark {
		return
	}
	if dark {
		e.dark[pool] = true
	} else {
		delete(e.dark, pool)
	}
	e.jr.Emit(at, KindPoolTier, pool, PoolTier{Dark: dark, Why: why})
}

// poolStatus applies an observed pod state, then recovers.
func (e *Engine) poolStatus(ctx context.Context, at sched.Time, s PoolStatus) error {
	id := population.ActorID(s.Pool)
	if _, ok := e.co.Ring.PoolOf(id); !ok {
		return nil
	}
	if s.Running {
		delete(e.down, id)
		delete(e.repairs, id)
	} else {
		e.down[id] = true
	}
	return e.recover(ctx, at)
}

// recover runs the guard's recovery for the state at at and starts it:
// GoLive and repairs at once, a move for each stranded wallet. A frozen
// ledger is a stall incident instead.
func (e *Engine) recover(ctx context.Context, at sched.Time) error {
	s := e.floorState(at)
	if e.stalled || len(s.Pools) == 0 {
		return nil
	}
	if s.Frozen() {
		e.stalled = true
		e.jr.Emit(at, KindIncident, 0, Incident{Kind: "stall", Epoch: s.Epoch, Label: s.Label})
		return nil
	}
	plan := e.guard.Recover(s)
	for _, pool := range plan.GoLive {
		e.setTier(at, pool, false, "recovery")
	}
	for _, pool := range plan.Repair {
		e.repair(at, pool)
	}
	for _, mv := range plan.Moves {
		if err := e.move1(ctx, at, mv); err != nil {
			return err
		}
	}
	return nil
}

// repair asks for a dead pod to be brought back, once per death. With
// simulated pods the pod is back at once.
func (e *Engine) repair(at sched.Time, pool population.ActorID) {
	if e.repairs[pool] {
		return
	}
	e.jr.Emit(at, KindRepair, pool, Repair{})
	if e.cfg.SimulatedPods {
		delete(e.down, pool)
		return
	}
	e.repairs[pool] = true
}

// move1 starts a recovery move: the engine's own delegation of a stranded
// wallet onto a forging pool. It needs no agenda and is never guarded.
func (e *Engine) move1(ctx context.Context, at sched.Time, mv policy.Intent) error {
	a, ok := e.co.Get(mv.Wallet)
	if !ok {
		return nil
	}
	p := &pendingRec{Actor: uint64(a.ID), Kind: string(profile.IntentDelegate), To: uint64(mv.Pool), Register: !e.win.View().StakeRegistered(a.ID)}
	why, err := e.start(ctx, at, p)
	if err != nil {
		return err
	}
	if why != "" {
		e.jr.Emit(at, KindSkipped, a.ID, Skipped{Kind: "recovery_move", Why: why})
	}
	return nil
}

// act1Pool takes an operator's go-dark or go-live, which needs no
// transaction, after the guard allows it.
func (e *Engine) act1Pool(at sched.Time, in agenda.Intent) {
	e.setTier(at, in.Actor, in.Kind == agenda.GoDark, "agenda")
}

// Stalled reports a stall incident: the run must stop.
func (e *Engine) Stalled() bool { return e.stalled }

// Plan is the engine's pool plan: the pool actors it wants dark, ascending.
func (e *Engine) Plan() []population.ActorID {
	out := make([]population.ActorID, 0, len(e.dark))
	for id := range e.dark {
		out = append(out, id)
	}
	slices.Sort(out)
	return out
}

// checkFloor asks the guard about an intent an agenda chose, recording a
// refusal.
func (e *Engine) checkFloor(at sched.Time, in agenda.Intent) bool {
	vd := e.allows(at, in)
	if !vd.OK {
		e.jr.Emit(at, KindGuarded, in.Actor, Guarded{Kind: string(in.Kind), Target: uint64(in.Target), Why: vd.Why})
	}
	return vd.OK
}
