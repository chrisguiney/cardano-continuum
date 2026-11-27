package feeds

import (
	"context"
	"fmt"

	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/wire"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

// PodSource reports the pool pods' observed health: the pools whose pod
// started or stopped running since the last call. A live run reads the
// operator's ContinuumPool status; tests script it.
type PodSource interface {
	Changes(ctx context.Context, now sched.Time) ([]wire.PoolStatus, error)
}

// Pods feeds every pod health change as a PoolStatus input.
type Pods struct {
	src   PodSource
	clock Clock
}

// NewPods returns the pod feed; a nil src never observes a pod down.
func NewPods(src PodSource, c Clock) Pods { return Pods{src: src, clock: c} }

// Poll feeds the changes since the last call, each followed by the
// submissions it decides.
func (p Pods) Poll(ctx context.Context, l Loop) error {
	if p.src == nil {
		return nil
	}
	now := p.clock.Now()
	ss, err := p.src.Changes(ctx, now)
	if err != nil {
		return fmt.Errorf("engine: pod status: %w", err)
	}
	for _, s := range ss {
		body, err := eventlog.Encode(s)
		if err != nil {
			return err
		}
		rec := eventlog.Record{At: now, Kind: eventlog.KindPoolStatus, Actor: eventlog.ActorID(s.Pool), Body: body}
		if err := l.Feed(ctx, rec); err != nil {
			return err
		}
		if err := l.Effects(ctx); err != nil {
			return err
		}
	}
	return nil
}
