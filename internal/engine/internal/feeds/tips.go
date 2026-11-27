package feeds

import (
	"context"

	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/wire"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/oracle"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

// TipSource reports each followed pool's newest blocks. A live run's is the
// pool tracker; tests script one.
type TipSource interface {
	Branches(ctx context.Context) []oracle.Branch
}

// Tips logs the pools' followed chains as a MonitorSnapshot input every
// interval, for the fork monitor. Nothing is logged while no pool is
// followed.
type Tips struct {
	src    TipSource
	clock  Clock
	every  sched.Time
	at     sched.Time // of the last ask
	tipped bool
}

// NewTips returns the tip feed, asking src every every of simulated time;
// a nil src asks nothing.
func NewTips(src TipSource, c Clock, every sched.Time) *Tips {
	return &Tips{src: src, clock: c, every: every}
}

// Poll asks the source when the interval has passed.
func (t *Tips) Poll(ctx context.Context, l Loop) error {
	if t.src == nil {
		return nil
	}
	now := t.clock.Now()
	if t.tipped && now < t.at+t.every {
		return nil
	}
	t.at, t.tipped = now, true
	bs := t.src.Branches(ctx)
	if len(bs) == 0 {
		return nil
	}
	body, err := eventlog.Encode(wire.MonitorSnapshot{Tips: bs})
	if err != nil {
		return err
	}
	return l.Feed(ctx, eventlog.Record{At: l.Now(), Kind: eventlog.KindMonitorSnapshot, Body: body})
}
