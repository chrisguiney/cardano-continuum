package engine

import (
	"context"
	"errors"

	"github.com/chrisguiney/cardano-continuum/internal/agenda"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/wire"
	"github.com/chrisguiney/cardano-continuum/internal/policy"
	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

// This file routes the stake floor (internal/engine/internal/floor) into
// the engine: pod observations, the recovery run after every block and pod
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
