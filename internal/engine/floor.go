package engine

import (
	"errors"

	"github.com/chrisguiney/cardano-continuum/internal/agenda"
	"github.com/chrisguiney/cardano-continuum/internal/policy"
	"github.com/chrisguiney/cardano-continuum/internal/population"
)

// This file adapts the engine to the stake floor
// (internal/engine/internal/floor). The pod observations and the recovery run
// after every block and pod event are Engine.poolStatus and Engine.recover;
// the recovery moves, which are the engine's own intents, start through
// starter.move.

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
