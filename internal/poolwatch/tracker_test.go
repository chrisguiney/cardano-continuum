package poolwatch

import (
	"context"
	"errors"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/chrisguiney/cardano-continuum/internal/chain"
	"go.uber.org/goleak"
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

// The tests run in a synctest bubble: Wait returns once every goroutine is
// blocked, so a tip is read only after the tracker has settled, and the fake
// clock moves only when the test sleeps.
func TestTrackerFollowsEachTarget(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		d := &fakeDial{dials: map[string]int{}, conns: map[string][]*fakeConn{
			"a:3001": {{tip: mk(1, 'a'), script: []Event{fwd(mk(2, 'a')), fwd(mk(3, 'a'))}}},
			"b:3001": {{tip: mk(1, 'a'), script: []Event{fwd(mk(2, 'b'))}}},
		}}
		targets := func(context.Context) ([]Target, error) {
			return []Target{{Name: "p0", Addr: "a:3001"}, {Name: "p1", Addr: "b:3001"}}, nil
		}
		tr := New(d, targets, Config{Size: 8, Refresh: 10 * time.Millisecond, Backoff: time.Millisecond})
		defer run(t, tr)()
		synctest.Wait()
		tips := tr.Tips()
		if len(tips["p0"]) != 3 || len(tips["p1"]) != 2 {
			t.Fatalf("tips %+v", tips)
		}
		if tips["p1"][1].Hash[0] != 'b' {
			t.Fatalf("p1 %+v", tips["p1"])
		}
	})
}

func TestTrackerReconnectsAndDropsRemovedTarget(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
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
		cfg := Config{Size: 8, Refresh: 10 * time.Millisecond, Backoff: time.Millisecond}
		tr := New(d, targets, cfg)
		defer run(t, tr)()
		time.Sleep(cfg.Backoff)
		synctest.Wait()
		if m := tr.Tips()["p0"]; len(m) != 1 || m[0].Height != 7 {
			t.Fatalf("after reconnect: %+v", m)
		}
		mu.Lock()
		live = false
		mu.Unlock()
		time.Sleep(cfg.Refresh)
		synctest.Wait()
		if tips := tr.Tips(); len(tips) != 0 {
			t.Fatalf("pool not dropped: %+v", tips)
		}
	})
}

func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

func bwd(m chain.Mark) Event { return Event{Backward: &m.Point} }

func TestTrackerCountsRollbacksNotHandshakesOrReconnects(t *testing.T) {
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		d := &fakeDial{dials: map[string]int{}, conns: map[string][]*fakeConn{
			"a:3001": {
				// The handshake rollback to the tip, then two real rollbacks.
				{tip: mk(5, 'a'), script: []Event{
					bwd(mk(5, 'a')), fwd(mk(6, 'a')), fwd(mk(7, 'a')), bwd(mk(5, 'a')),
					fwd(mk(6, 'b')), bwd(mk(6, 'b')), bwd(mk(5, 'a')), {Err: errors.New("reset")},
				}},
				// A node that restarted behind its old tip: a lower intersect, not a rollback.
				{tip: mk(2, 'a'), script: []Event{bwd(mk(2, 'a'))}},
			},
		}}
		targets := func(context.Context) ([]Target, error) {
			return []Target{{Name: "p0", Addr: "a:3001"}}, nil
		}
		cfg := Config{Size: 8, Refresh: 10 * time.Millisecond, Backoff: time.Millisecond}
		tr := New(d, targets, cfg)
		defer run(t, tr)()
		time.Sleep(cfg.Backoff * 2)
		synctest.Wait()
		l := tr.Rollbacks()["p0"]
		if l.Count != 2 || l.Deepest.Blocks != 2 || l.Last.Blocks != 1 || l.Last.To.Slot != 50 || len(l.Recent) != 2 {
			t.Fatalf("log %+v", l)
		}
		if d.dials["a:3001"] < 2 {
			t.Fatalf("no reconnect: %d dials", d.dials["a:3001"])
		}
	})
}
