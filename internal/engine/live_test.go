package engine

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/blinklabs-io/gouroboros/ledger/common"

	"github.com/chrisguiney/cardano-continuum/internal/chain"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/keys"
	"github.com/chrisguiney/cardano-continuum/internal/rng"
)

// scriptSource is one chain-sync connection that answers Intersect with at,
// delivers events, then fails with err (nil: blocks until ctx ends).
type scriptSource struct {
	at     chain.Point
	events []chain.Event
	err    error
	offers *[][]chain.Point
}

func (s *scriptSource) Intersect(_ context.Context, pts []chain.Point) (chain.Point, error) {
	*s.offers = append(*s.offers, pts)
	return s.at, nil
}

func (s *scriptSource) Next(ctx context.Context) (chain.Event, error) {
	if len(s.events) > 0 {
		ev := s.events[0]
		s.events = s.events[1:]
		return ev, nil
	}
	if s.err != nil {
		return chain.Event{}, s.err
	}
	<-ctx.Done()
	return chain.Event{}, ctx.Err()
}

func (*scriptSource) Close() error { return nil }

// scriptDialer hands out its sources in order.
type scriptDialer struct {
	mu sync.Mutex
	// protects srcs, offers
	srcs   []*scriptSource
	offers [][]chain.Point
}

func (d *scriptDialer) Dial(context.Context) (chain.Source, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.srcs) == 0 {
		return nil, errors.New("no more connections")
	}
	s := d.srcs[0]
	d.srcs = d.srcs[1:]
	s.offers = &d.offers
	return s, nil
}

type recTarget struct {
	era uint
	tx  []byte
}

func (r *recTarget) Submit(_ context.Context, era uint, tx []byte) error {
	r.era, r.tx = era, tx
	return nil
}

func (*recTarget) Name() string { return "rec" }

func livePoint(slot uint64, b byte) chain.Point {
	var h common.Blake2b256
	h[0] = b
	return chain.Point{Slot: slot, Hash: h}
}

// next polls f until it delivers an event: the follower goroutine sends
// asynchronously, so caught up is momentary.
func next(t *testing.T, f Follower) ChainEvent {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		ev, ok, err := f.Next()
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			return ev
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("no chain event within 5s")
	return ChainEvent{}
}

func TestLiveFollowsAndResumes(t *testing.T) {
	defer goleak.VerifyNone(t)
	var seed rng.Seed
	ks, err := keys.New(seed, 0)
	if err != nil {
		t.Fatal(err)
	}
	addr := ks.Wallet(1).Address()
	p0, p1, p2 := livePoint(10, 1), livePoint(11, 2), livePoint(12, 3)
	tx := chain.Tx{Hash: common.Blake2b256{9}, Spends: []chain.Ref{{Tx: common.Blake2b256{8}, Index: 1}},
		Creates: []common.Utxo{ownedUtxo(addr, Ref{Tx: TxID{9}, Index: 0}, 5_000_000)}, Fee: 170_000}
	d := &scriptDialer{srcs: []*scriptSource{
		// The connection drops after a block and a rollback.
		{at: p0, events: []chain.Event{
			{Kind: chain.Forward, Block: chain.Block{Point: p1, Txs: []chain.Tx{tx}}, Point: p1},
			{Kind: chain.Backward, Point: p0},
		}, err: errors.New("connection reset")},
		// The redial re-intersects at the last point: not news. Then p2.
		{at: p0, events: []chain.Event{{Kind: chain.Forward, Block: chain.Block{Point: p2}, Point: p2}}},
	}}
	target := &recTarget{}
	l := NewLive(d, target, 5, nil)
	l.backoff = func(int) time.Duration { return 0 }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	l.Start(ctx)

	f, at, err := l.Intersect([]eventlog.ChainPoint{p0.Log(), {}})
	if err != nil {
		t.Fatal(err)
	}
	if !samePoint(at, p0.Log()) {
		t.Fatalf("intersect at %+v, want %+v", at, p0.Log())
	}
	ev := next(t, f)
	if ev.Rollback || !samePoint(ev.Point, p1.Log()) {
		t.Fatalf("first event %+v, want block at %+v", ev, p1)
	}
	var c BlockContent
	if err := eventlog.Decode(ev.Content, &c); err != nil {
		t.Fatal(err)
	}
	wa, _ := addr.Bytes()
	if len(c.Txs) != 1 || c.Txs[0].Fee != 170_000 || c.Txs[0].Inputs[0] != (Ref{Tx: TxID{8}, Index: 1}) ||
		string(c.Txs[0].Outputs[0].Address) != string(wa) || c.Txs[0].Outputs[0].Lovelace != 5_000_000 {
		t.Fatalf("digest %+v", c)
	}
	if ev := next(t, f); !ev.Rollback || !samePoint(ev.Point, p0.Log()) {
		t.Fatalf("second event %+v, want rollback to %+v", ev, p0)
	}
	if ev := next(t, f); ev.Rollback || !samePoint(ev.Point, p2.Log()) {
		t.Fatalf("third event %+v, want block at %+v (the reconnect is not news)", ev, p2)
	}
	d.mu.Lock()
	if len(d.offers) != 2 || d.offers[0][0] != p0 || d.offers[1][0] != p0 {
		t.Errorf("offers %v, want the engine's points, then a resume at the rolled-back tip", d.offers)
	}
	d.mu.Unlock()

	if err := l.Submit(ctx, []byte{1, 2}); err != nil || target.era != conwayEra || len(target.tx) != 2 {
		t.Fatalf("submit: %v, era %d", err, target.era)
	}
	if l.HasTx(TxID{}) {
		t.Fatal("HasTx without a mempool query")
	}
	cancel()
	closeCtx, done := context.WithTimeout(context.Background(), 5*time.Second)
	defer done()
	if err := l.Close(closeCtx); err != nil {
		t.Fatal(err)
	}
}

// A node without any of the engine's points is fatal: the follower never
// falls back to the origin.
func TestLiveNoIntersection(t *testing.T) {
	defer goleak.VerifyNone(t)
	d := &noIntersect{}
	l := NewLive(d, &recTarget{}, 5, nil)
	l.Start(context.Background())
	if _, _, err := l.Intersect([]eventlog.ChainPoint{livePoint(3, 1).Log()}); !errors.Is(err, chain.ErrNoIntersect) {
		t.Fatalf("intersect = %v, want ErrNoIntersect", err)
	}
	ctx, done := context.WithTimeout(context.Background(), 5*time.Second)
	defer done()
	if err := l.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

type noIntersect struct{}

func (noIntersect) Dial(context.Context) (chain.Source, error) { return noIntersect{}, nil }
func (noIntersect) Intersect(context.Context, []chain.Point) (chain.Point, error) {
	return chain.Point{}, chain.ErrNoIntersect
}
func (noIntersect) Next(context.Context) (chain.Event, error) {
	return chain.Event{}, errors.New("unused")
}
func (noIntersect) Close() error { return nil }
