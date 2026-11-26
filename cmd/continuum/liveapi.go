package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"

	"github.com/chrisguiney/cardano-continuum/internal/engine"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/httpapi"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
	"github.com/chrisguiney/cardano-continuum/internal/store"
	"github.com/chrisguiney/cardano-continuum/web"
)

// This file serves the web UI from a live run (continuum run --serve): the
// engine's own log feeds the projection and the decision hub as it is
// appended, and POST /api/control queues controls the loop logs as inputs,
// so the run stays replayable.

// maxQueuedControls bounds the controls a server tracks for idempotency.
const maxQueuedControls = 10_000

// controlQueue is httpapi.Controls for a live engine and its
// engine.ControlSource: Submit queues, the loop drains.
type controlQueue struct {
	mu      sync.Mutex // protects seen, pending
	seen    map[string]bool
	pending [][]byte
}

func newControlQueue() *controlQueue { return &controlQueue{seen: map[string]bool{}} }

// Submit implements httpapi.Controls. The receipt has no seq: the loop
// gives the control one when it logs it.
func (q *controlQueue) Submit(_ context.Context, c httpapi.Control) (httpapi.Receipt, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.seen[c.RequestID] {
		return httpapi.Receipt{Duplicate: true}, nil
	}
	if len(q.seen) >= maxQueuedControls {
		return httpapi.Receipt{}, httpapi.ErrTooManyControls
	}
	body, err := eventlog.Encode(c)
	if err != nil {
		return httpapi.Receipt{}, fmt.Errorf("encode control %s: %w", c.RequestID, err)
	}
	q.seen[c.RequestID] = true
	q.pending = append(q.pending, body)
	return httpapi.Receipt{}, nil
}

// Controls implements engine.ControlSource.
func (q *controlQueue) Controls(context.Context) ([][]byte, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := q.pending
	q.pending = nil
	return out, nil
}

// feedLog is the engine's log, feeding every record it appends to the
// live API's projection and hub.
type feedLog struct {
	engine.Log
	feed *httpapi.Feed
}

// Append implements eventlog.Writer. The log numbers records in order, so
// the appended ones end at LastSeq.
func (l feedLog) Append(ctx context.Context, recs ...eventlog.Record) error {
	if err := l.Log.Append(ctx, recs...); err != nil {
		return err
	}
	first := l.LastSeq() - uint64(len(recs)) + 1
	for i, r := range recs {
		r.Seq = first + uint64(i)
		l.feed.Apply(r)
	}
	return nil
}

// liveServer is the API of a live run.
type liveServer struct {
	log      feedLog
	controls *controlQueue
	src      *httpapi.LogSource
	hub      *httpapi.Hub
}

// newLiveServer feeds the records already in l, then wraps l so later ones
// follow.
func newLiveServer(ctx context.Context, l engine.Log, db *store.DB, slots sched.Slots) (*liveServer, error) {
	proj, hub := httpapi.NewProjection(), httpapi.NewHub(4096, 64, 256)
	feed := httpapi.NewFeed(proj, hub)
	if err := l.Scan(ctx, 1, func(r eventlog.Record) error { feed.Apply(r); return nil }); err != nil {
		hub.Close()
		return nil, fmt.Errorf("read event log: %w", err)
	}
	cfg := httpapi.LogSourceConfig{Projection: proj, Hub: hub, Slots: &slots}
	if db != nil {
		cfg.Names = db
	}
	src := httpapi.NewLogSource(cfg)
	return &liveServer{log: feedLog{Log: l, feed: feed}, controls: newControlQueue(), src: src, hub: hub}, nil
}

// serve runs the API on addr until ctx ends.
func (s *liveServer) serve(ctx context.Context, addr string, log *slog.Logger) error {
	defer s.hub.Close()
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	h := httpapi.NewHandler(httpapi.Config{Source: s.src, Controls: s.controls, UI: web.Handler(), Logger: log})
	if err := httpapi.Serve(ctx, ln, h, log); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}
