// Package live is the engine's node adapter over a running cardano-node.
package live

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/chrisguiney/cardano-continuum/internal/chain"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/wire"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/submit"
)

// conwayEra is Conway's hard-fork-combinator era index, which
// local-tx-submission tags a transaction with.
const conwayEra = 6

// Node is the engine's node over a running cardano-node: blocks arrive through
// internal/chain's Follower (node-to-client chain-sync, redialled with
// backoff) and transactions leave through a submit.Target (local-tx-
// submission). Only where the inputs come from changes; the Runner logs them
// and Engine.Handle decides exactly as on the fake chain.
//
// It has the Start/Close shape: the follower goroutine Intersect starts runs
// under Start's context and Close joins it.
type Node struct {
	dial    chain.Dialer
	target  submit.Target
	k       uint64
	log     *slog.Logger
	backoff func(int) time.Duration

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	// stop ends the current follower; only the Runner's goroutine calls
	// Intersect, so it needs no lock.
	stop context.CancelFunc
}

// New returns a Node following d and submitting to t. k is the
// security parameter, which bounds the points the follower keeps. A nil log
// discards.
func New(d chain.Dialer, t submit.Target, k uint64, log *slog.Logger) *Node {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Node{dial: d, target: t, k: k, log: log}
}

// Start sets the context every follower runs under.
func (l *Node) Start(ctx context.Context) {
	l.ctx, l.cancel = context.WithCancel(ctx)
}

// Close stops the follower and waits for it, bounded by ctx.
func (l *Node) Close(ctx context.Context) error {
	l.cancel()
	done := make(chan struct{})
	go func() {
		l.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("engine: close live node: %w", ctx.Err())
	}
}

// Submit implements the engine's Node. A refusal comes back as *submit.RejectedError.
func (l *Node) Submit(ctx context.Context, tx []byte) error {
	return l.target.Submit(ctx, conwayEra, tx)
}

// HasTx implements the engine's Node. It always reports false: asking the mempool would
// need local-tx-monitor, and the cost of not asking is only that a
// submission resumed after a crash is sent again, where the retry machine
// already treats a rejection of a resend as proving nothing.
func (*Node) HasTx(wire.TxID) bool { return false }

// Intersect implements the engine's Node. It starts a follower from points (newest
// first) and blocks until the node answers FindIntersect; a node that is not
// up yet is redialled with backoff until it is, or Start's context ends.
func (l *Node) Intersect(points []eventlog.ChainPoint) (wire.Follower, eventlog.ChainPoint, error) {
	if l.stop != nil {
		l.stop()
	}
	start := make([]chain.Point, 0, len(points))
	for _, p := range slices.Backward(points) {
		cp, ok := chain.PointFromLog(p)
		if !ok {
			return nil, eventlog.ChainPoint{}, fmt.Errorf("%w: slot %d", wire.ErrBadPoint, p.Slot)
		}
		start = append(start, cp)
	}
	ctx, stop := context.WithCancel(l.ctx)
	l.stop = stop
	events := make(chan chain.Event)
	errc := make(chan error, 1)
	f := chain.NewFollower(l.dial, chain.FollowerConfig{K: l.k, Start: start, Backoff: l.backoff}, events, l.log)
	l.wg.Go(func() { errc <- f.Run(ctx) })
	select {
	case ev := <-events:
		// The follower's first event is always its FindIntersect answer.
		return &liveFollower{events: events, errc: errc, last: ev.Point}, ev.Point.Log(), nil
	case err := <-errc:
		return nil, eventlog.ChainPoint{}, fmt.Errorf("engine: follow: %w", err)
	}
}

// liveFollower adapts the follower's channel to wire.Follower's polling Next.
type liveFollower struct {
	events <-chan chain.Event
	errc   <-chan error
	err    error
	// last is the last point delivered, so a reconnect that re-intersects
	// there is not logged as a rollback.
	last chain.Point
}

// Next implements wire.Follower. A follower stopped by cancellation reports
// caught up rather than an error, so the Runner shuts down cleanly through
// its own context.
func (f *liveFollower) Next() (wire.ChainEvent, bool, error) {
	if f.err != nil {
		return wire.ChainEvent{}, false, f.err
	}
	for {
		var ev chain.Event
		select {
		case ev = <-f.events:
		case err := <-f.errc:
			if errors.Is(err, context.Canceled) {
				return wire.ChainEvent{}, false, nil
			}
			f.err = err
			return wire.ChainEvent{}, false, err
		default:
			return wire.ChainEvent{}, false, nil
		}
		switch ev.Kind {
		case chain.Forward:
			content, err := wire.DigestOf(ev.Block)
			if err != nil {
				return wire.ChainEvent{}, false, err
			}
			f.last = ev.Block.Point
			return wire.ChainEvent{Point: ev.Block.Point.Log(), Content: content}, true, nil
		case chain.Backward, chain.Intersected:
			// A reconnect intersecting at the last point delivered changes
			// nothing; one below it is the node's rollback while away.
			if ev.Kind == chain.Intersected && ev.Point == f.last {
				continue
			}
			f.last = ev.Point
			return wire.ChainEvent{Rollback: true, Point: ev.Point.Log()}, true, nil
		}
	}
}
