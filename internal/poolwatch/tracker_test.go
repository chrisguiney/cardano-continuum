package poolwatch

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/chrisguiney/cardano-continuum/internal/chain"
)

// fakeConn replays script, then blocks until its context ends.
type fakeConn struct {
	tip    chain.Mark
	script []Event
}

func (c *fakeConn) Start(context.Context) (chain.Mark, error) { return c.tip, nil }

func (c *fakeConn) Next(ctx context.Context) (Event, error) {
	if len(c.script) == 0 {
		<-ctx.Done()
		return Event{}, ctx.Err()
	}
	ev := c.script[0]
	c.script = c.script[1:]
	return ev, nil
}

func (c *fakeConn) Close() error { return nil }

type fakeDial struct {
	mu    sync.Mutex // protects conns, dials
	conns map[string][]*fakeConn
	dials map[string]int
}

func (d *fakeDial) Dial(_ context.Context, addr string) (Conn, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.dials[addr]++
	cs := d.conns[addr]
	if len(cs) == 0 {
		return nil, errors.New("no node")
	}
	c := cs[0]
	if len(cs) > 1 {
		d.conns[addr] = cs[1:]
	}
	return c, nil
}

func fwd(m chain.Mark) Event { return Event{Forward: &m} }

func run(t *testing.T, tr *Tracker) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- tr.Run(ctx) }()
	return func() {
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Errorf("run: %v", err)
		}
	}
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for range 400 {
		if ok() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestTrackerFollowsEachTarget(t *testing.T) {
	d := &fakeDial{dials: map[string]int{}, conns: map[string][]*fakeConn{
		"a:3001": {{tip: mk(1, 'a'), script: []Event{fwd(mk(2, 'a')), fwd(mk(3, 'a'))}}},
		"b:3001": {{tip: mk(1, 'a'), script: []Event{fwd(mk(2, 'b'))}}},
	}}
	targets := func(context.Context) ([]Target, error) {
		return []Target{{Name: "p0", Addr: "a:3001"}, {Name: "p1", Addr: "b:3001"}}, nil
	}
	tr := New(d, targets, Config{Size: 8, Refresh: 10 * time.Millisecond, Backoff: time.Millisecond})
	defer run(t, tr)()
	waitFor(t, "both tips", func() bool {
		tips := tr.Tips()
		return len(tips["p0"]) == 3 && len(tips["p1"]) == 2
	})
	if tr.Tips()["p1"][1].Hash[0] != 'b' {
		t.Fatalf("p1 %+v", tr.Tips()["p1"])
	}
}

func TestTrackerReconnectsAndDropsRemovedTarget(t *testing.T) {
	d := &fakeDial{dials: map[string]int{}, conns: map[string][]*fakeConn{
		"a:3001": {
			{tip: mk(1, 'a'), script: []Event{{Err: errors.New("reset")}}},
			{tip: mk(7, 'a')},
		},
	}}
	var mu sync.Mutex // protects live
	live := true
	targets := func(context.Context) ([]Target, error) {
		mu.Lock()
		defer mu.Unlock()
		if !live {
			return nil, nil
		}
		return []Target{{Name: "p0", Addr: "a:3001"}}, nil
	}
	tr := New(d, targets, Config{Size: 8, Refresh: 10 * time.Millisecond, Backoff: time.Millisecond})
	defer run(t, tr)()
	waitFor(t, "reconnected tip", func() bool {
		m := tr.Tips()["p0"]
		return len(m) == 1 && m[0].Height == 7
	})
	mu.Lock()
	live = false
	mu.Unlock()
	waitFor(t, "pool dropped", func() bool { return len(tr.Tips()) == 0 })
}
