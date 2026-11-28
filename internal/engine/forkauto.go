package engine

import (
	"context"

	"github.com/chrisguiney/cardano-continuum/internal/protocol"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

// This file is the run's own hard fork (Config.HardFork). At each epoch
// boundary until the version is reached, the network proposer gets the
// committee's hot keys authorised and, from the scheduled epoch on and once
// governance is ready, proposes the fork through the hard_fork scenario's
// path (scenarioRoute.HardFork), so the one-fork-at-a-time guard and the
// builder are the manual scenario's. A refusal or a proposal the ledger
// rejects or lets expire is retried at the next boundary: one attempt per
// epoch is the backoff, and an attempt is skipped while a fork is open,
// being proposed or ratified.
//
// The step reads only the chain view, the in-flight payments and the
// configuration, so a restart arms the next boundary from the restored
// clock and needs nothing of its own in the snapshot.

// armFork schedules the next fork step at the first epoch boundary after
// from. It does nothing without a schedule. New arms from time zero and
// Restore from the restored clock, so the step needs no snapshot field.
func (e *Engine) armFork(from sched.Time) {
	if e.cfg.HardFork.On() {
		evsched{&e.clk}.Fork(e.cfg.Slots.NextEpochStart(from))
	}
}

// forkStep runs at an epoch boundary and arms the next one.
func (e *Engine) forkStep(ctx context.Context, at sched.Time) error {
	e.armFork(at)
	v := e.win.View()
	if v == nil {
		return nil
	}

	cur := v.VersionAt(v.Epoch())
	if !cur.Less(e.cfg.HardFork.To) {
		return nil
	}

	route := scenarioRoute{e}
	if cur.Major < protocol.VanRossem && !e.committeeReady() {
		_, err := route.HotKeys(ctx, at)
		return err
	}

	if !e.cfg.HardFork.Due(v.Epoch(), cur) || !e.poolsReady() {
		return nil
	}

	_, err := route.HardFork(ctx, at, e.cfg.HardFork.To.String())
	return err
}

// committeeReady reports that every committee seat's hot key is
// authorised, which a committee vote needs below protocol version 11.
func (e *Engine) committeeReady() bool {
	v := e.win.View()
	for i := range e.cfg.Committee {
		if !v.HotAuthorized(i) {
			return false
		}
	}
	return true
}

// poolsReady reports that some registered pool holds stake, so its vote
// carries power.
func (e *Engine) poolsReady() bool {
	for _, p := range e.win.View().Pools() {
		if p.Stake > 0 {
			return true
		}
	}
	return false
}
