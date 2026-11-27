package node

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"time"

	"github.com/chrisguiney/cardano-continuum/internal/chain"
)

// ErrNoIntersect: the node has none of the offered points. The follower
// never falls back to the origin, because rebuilding the view from there
// would offer outputs the planner has settled as spent
// (PlannerRestartFromOriginBug in models/specs/planner-reservations).
var ErrNoIntersect = errors.New("chain: no intersection with the node's chain")

// Source is one chain-sync connection.
type Source interface {
	// Intersect finds the first of points (newest first) on the node's
	// chain, positions the stream after it and returns it, or
	// ErrNoIntersect.
	Intersect(ctx context.Context, points []chain.Point) (chain.Point, error)
	// Next blocks until the next roll forward or backward.
	Next(ctx context.Context) (chain.Event, error)
	Close() error
}

// Dialer opens chain-sync connections.
type Dialer interface {
	Dial(ctx context.Context) (Source, error)
}

// FollowerConfig configures a Follower.
type FollowerConfig struct {
	// K bounds how many points the follower keeps for re-intersecting.
	K uint64
	// Start are the stored intersection points, oldest first, reaching down
	// to the settled point. Empty means a fresh start from the origin.
	Start []chain.Point
	// Backoff is the wait before reconnect attempt n (from 0). Nil means
	// doubling from one second up to thirty.
	Backoff func(attempt int) time.Duration
}

// Follower reads the node's chain and sends every block, rollback and
// intersection answer to out, in order. It is the I/O shell around the
// view: it keeps only the points it needs to resume, and leaves applying
// events to the engine, which logs each one first.
type Follower struct {
	dial    Dialer
	out     chan<- chain.Event
	log     *slog.Logger
	backoff func(int) time.Duration
	keep    int
	// points are the blocks delivered, oldest first, at most keep long.
	// Only Run's goroutine touches them.
	points []chain.Point
}

// NewFollower returns a follower; Run starts it. A nil log discards.
func NewFollower(d Dialer, cfg FollowerConfig, out chan<- chain.Event, log *slog.Logger) *Follower {
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	b := cfg.Backoff
	if b == nil {
		b = defaultBackoff
	}
	keep := int(cfg.K) + 1
	pts := slices.Clone(cfg.Start)
	keep = max(keep, len(pts))
	return &Follower{dial: d, out: out, log: log.With("component", "chain"), backoff: b, keep: keep, points: pts}
}

func defaultBackoff(n int) time.Duration {
	return min(time.Second<<min(n, 5), 30*time.Second)
}

// Run follows until ctx is cancelled or the node has none of the
// follower's points. A dropped connection is redialled with backoff and
// re-intersected at the last delivered points, so no block is delivered
// twice and none is skipped.
func (f *Follower) Run(ctx context.Context) error {
	for attempt := 0; ; attempt++ {
		progressed, err := f.session(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if errors.Is(err, ErrNoIntersect) {
			return err
		}
		if progressed {
			attempt = 0
		}
		wait := f.backoff(attempt)
		f.log.Warn("chain-sync connection lost", "err", err, "retry_in", wait)
		if err := sleep(ctx, wait); err != nil {
			return err
		}
	}
}

// session runs one connection and reports whether it delivered anything.
func (f *Follower) session(ctx context.Context) (bool, error) {
	src, err := f.dial.Dial(ctx)
	if err != nil {
		return false, fmt.Errorf("chain: dial: %w", err)
	}
	defer func() {
		if err := src.Close(); err != nil {
			f.log.Debug("close chain-sync connection", "err", err)
		}
	}()
	at, err := src.Intersect(ctx, f.offer())
	if err != nil {
		return false, fmt.Errorf("chain: intersect: %w", err)
	}
	f.rollTo(at)
	if err := f.send(ctx, chain.Event{Kind: chain.Intersected, Point: at}); err != nil {
		return false, err
	}
	for progressed := false; ; progressed = true {
		ev, err := src.Next(ctx)
		if err != nil {
			return progressed, fmt.Errorf("chain: next: %w", err)
		}
		if !f.track(ev) {
			continue
		}
		if err := f.send(ctx, ev); err != nil {
			return progressed, err
		}
	}
}

// track updates the points for ev and reports whether ev is news. A node
// answers the first request after an intersection with a rollback to the
// intersection itself; that, and any rollback to the current tip, is not.
func (f *Follower) track(ev chain.Event) bool {
	switch ev.Kind {
	case chain.Forward:
		f.points = append(f.points, ev.Block.Point)
		if len(f.points) > f.keep {
			f.points = slices.Delete(f.points, 0, len(f.points)-f.keep)
		}
		return true
	case chain.Backward:
		if len(f.points) > 0 && f.points[len(f.points)-1] == ev.Point {
			return false
		}
		if len(f.points) == 0 && ev.Point.IsOrigin() {
			return false
		}
		f.rollTo(ev.Point)
		return true
	}
	return false
}

// rollTo drops the points above p. A p the follower does not hold becomes
// its only point; the view decides whether the rollback is legal.
func (f *Follower) rollTo(p chain.Point) {
	if i := slices.Index(f.points, p); i >= 0 {
		f.points = f.points[:i+1]
		return
	}
	if p.IsOrigin() {
		f.points = nil
		return
	}
	f.points = []chain.Point{p}
}

// offer returns the points newest first; the origin only on a fresh start.
func (f *Follower) offer() []chain.Point {
	if len(f.points) == 0 {
		return []chain.Point{{}}
	}
	out := slices.Clone(f.points)
	slices.Reverse(out)
	return out
}

func (f *Follower) send(ctx context.Context, ev chain.Event) error {
	select {
	case f.out <- ev:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
