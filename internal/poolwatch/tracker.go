package poolwatch

import (
	"context"
	"errors"
	"log/slog"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/chrisguiney/cardano-continuum/internal/chain"
)

// Event is one chain-sync message from a node: a block to append, a
// rollback to a point, or the error that ended the connection.
type Event struct {
	Forward  *chain.Mark
	Backward *chain.Point
	Err      error
}

// Conn is one node-to-node chain-sync connection.
type Conn interface {
	// Start positions the stream at the node's tip and returns it.
	Start(ctx context.Context) (chain.Mark, error)
	// Next blocks for the next event or until ctx ends.
	Next(ctx context.Context) (Event, error)
	Close() error
}

// Dialer opens a Conn to a node-to-node address.
type Dialer interface {
	Dial(ctx context.Context, addr string) (Conn, error)
}

// Target is a pool to follow: its name and node-to-node address.
type Target struct{ Name, Addr string }

// Config sets how much a Tracker keeps and how often it works.
type Config struct {
	// Size is the number of blocks kept per pool.
	Size int
	// Refresh is how often the target list is re-read.
	Refresh time.Duration
	// Backoff is the wait before redialing a failed connection.
	Backoff time.Duration
}

// Tracker follows every Target and serves the newest blocks of each pool's
// chain. Tips is safe to call from any goroutine.
type Tracker struct {
	dial    Dialer
	targets func(ctx context.Context) ([]Target, error)
	cfg     Config
	log     *slog.Logger

	mu      sync.Mutex // protects windows, rolled
	windows map[string][]chain.Mark
	// rolled is each followed pool's rollbacks; it outlives a reconnect.
	rolled map[string]chain.RollbackLog
}

// New returns a Tracker. targets lists the pools to follow; it is called
// every cfg.Refresh and a pool that leaves the list stops being followed.
func New(d Dialer, targets func(ctx context.Context) ([]Target, error), cfg Config, log ...*slog.Logger) *Tracker {
	l := slog.Default()
	if len(log) > 0 {
		l = log[0]
	}
	return &Tracker{dial: d, targets: targets, cfg: cfg, log: l.With("component", "poolwatch"),
		windows: map[string][]chain.Mark{}, rolled: map[string]chain.RollbackLog{}}
}

// Tips returns each followed pool's newest blocks, oldest first. A pool
// whose connection is down keeps its last blocks until it is redialed.
func (t *Tracker) Tips() map[string][]chain.Mark {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make(map[string][]chain.Mark, len(t.windows))
	for k, v := range t.windows {
		out[k] = slices.Clone(v)
	}
	return out
}

// Rollbacks returns each followed pool's rollbacks since it was first
// followed. A reconnect does not count: the new connection starts at the
// node's tip, and only a RollBackward behind the tip it followed from counts.
func (t *Tracker) Rollbacks() map[string]chain.RollbackLog {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make(map[string]chain.RollbackLog, len(t.rolled))
	for k, v := range t.rolled {
		out[k] = v.Clone()
	}
	return out
}

func (t *Tracker) rollback(name string, r chain.Rollback) {
	t.mu.Lock()
	l := t.rolled[name]
	l.Add(r)
	t.rolled[name] = l
	t.mu.Unlock()
}

func (t *Tracker) set(name string, w *Window) {
	t.mu.Lock()
	t.windows[name] = w.Marks()
	t.mu.Unlock()
}

func (t *Tracker) drop(name string) {
	t.mu.Lock()
	delete(t.windows, name)
	delete(t.rolled, name)
	t.mu.Unlock()
}

type follower struct {
	addr   string
	cancel context.CancelFunc
}

// Run follows the targets until ctx ends, then returns ctx's error after
// every follower has exited.
func (t *Tracker) Run(ctx context.Context) error {
	var wg sync.WaitGroup
	running := map[string]follower{}
	defer func() {
		for _, f := range running {
			f.cancel()
		}
		wg.Wait()
	}()
	tick := time.NewTicker(t.cfg.Refresh)
	defer tick.Stop()
	for {
		t.reconcile(ctx, &wg, running)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}

// reconcile starts a follower for each new or re-addressed target and stops
// those no longer listed. A failed listing keeps the current set.
func (t *Tracker) reconcile(ctx context.Context, wg *sync.WaitGroup, running map[string]follower) {
	ts, err := t.targets(ctx)
	if err != nil {
		if ctx.Err() == nil {
			t.log.Warn("list pools", "err", err)
		}
		return
	}
	want := map[string]string{}
	for _, tg := range ts {
		want[tg.Name] = tg.Addr
	}
	for name, f := range running {
		if addr, ok := want[name]; !ok || addr != f.addr {
			f.cancel()
			delete(running, name)
			if !ok {
				t.drop(name)
			}
		}
	}
	for _, name := range slices.Sorted(maps.Keys(want)) {
		if _, ok := running[name]; ok {
			continue
		}
		fctx, cancel := context.WithCancel(ctx)
		running[name] = follower{addr: want[name], cancel: cancel}
		wg.Add(1)
		go func() {
			defer wg.Done()
			t.follow(fctx, name, want[name])
		}()
	}
}

// follow keeps one pool's window current, redialing after a failure.
func (t *Tracker) follow(ctx context.Context, name, addr string) {
	for ctx.Err() == nil {
		if err := t.session(ctx, name, addr); err != nil && ctx.Err() == nil {
			t.log.Warn("follow pool", "pool", name, "addr", addr, "err", err)
		}
		select {
		case <-ctx.Done():
		case <-time.After(t.cfg.Backoff):
		}
	}
}

func (t *Tracker) session(ctx context.Context, name, addr string) error {
	c, err := t.dial.Dial(ctx, addr)
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	tip, err := c.Start(ctx)
	if err != nil {
		return err
	}
	w := NewWindow(t.cfg.Size)
	w.Forward(tip)
	t.set(name, w)
	for {
		ev, err := c.Next(ctx)
		if err != nil {
			return err
		}
		switch {
		case ev.Err != nil:
			return ev.Err
		case ev.Forward != nil:
			w.Forward(*ev.Forward)
		case ev.Backward != nil:
			if rb, ok := w.Backward(*ev.Backward); ok {
				t.rollback(name, rb)
			}
		default:
			return errors.New("poolwatch: empty chain-sync event")
		}
		t.set(name, w)
	}
}
