package feeds

import (
	"context"
	"fmt"
	"slices"

	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/wire"
)

// PoolWriter applies the pool plan. Sync is idempotent: the loop calls it
// with the whole plan whenever the plan changed, and once after starting.
type PoolWriter interface {
	Sync(ctx context.Context, plan []wire.PoolPlan) error
}

// Planner is the engine's pool plan.
type Planner interface {
	Pools() []wire.PoolPlan
}

// Pools hands the pool plan to the writer when it changed.
type Pools struct {
	w      PoolWriter
	from   Planner
	plan   []wire.PoolPlan // last synced
	synced bool
}

// NewPools returns the pool plan feed; a nil w applies nothing.
func NewPools(w PoolWriter, from Planner) *Pools { return &Pools{w: w, from: from} }

// Poll syncs the plan if it changed since the last sync.
func (p *Pools) Poll(ctx context.Context) error {
	if p.w == nil {
		return nil
	}
	plan := p.from.Pools()
	if p.synced && slices.Equal(plan, p.plan) {
		return nil
	}
	if err := p.w.Sync(ctx, plan); err != nil {
		return fmt.Errorf("engine: sync pools: %w", err)
	}
	p.plan, p.synced = plan, true
	return nil
}
