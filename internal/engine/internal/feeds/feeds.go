// Package feeds turns what the world outside the chain reports (pod
// health, mempool drops, operator controls, node views and pool tips) into
// logged engine inputs, and hands the engine's pool plan to the operator.
// Each feed is its own small type; Set polls them in a fixed order, which
// is part of the determinism contract.
package feeds

import (
	"context"

	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

// Loop is the runner as a feed uses it.
type Loop interface {
	// Feed logs rec as an input and hands it to the engine.
	Feed(ctx context.Context, rec eventlog.Record) error
	// Effects performs every submission the engine is waiting on.
	Effects(ctx context.Context) error
	// Now is the engine's simulated time.
	Now() sched.Time
}

// Clock reads the run's clock.
type Clock interface {
	Now() sched.Time
}

// Set is every feed of a run.
type Set struct {
	View     *View
	Tips     *Tips
	Pods     Pods
	Drops    Drops
	Controls Controls
	Pools    *Pools
}

// Poll polls each feed once, in the order the log records them.
func (s *Set) Poll(ctx context.Context, l Loop) error {
	if err := s.View.Poll(ctx, l); err != nil {
		return err
	}
	if err := s.Tips.Poll(ctx, l); err != nil {
		return err
	}
	if err := s.Pods.Poll(ctx, l); err != nil {
		return err
	}
	if err := s.Drops.Poll(ctx, l); err != nil {
		return err
	}
	if err := s.Controls.Poll(ctx, l); err != nil {
		return err
	}
	return s.Pools.Poll(ctx)
}
