package node

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/blinklabs-io/gouroboros/cbor"
	"github.com/blinklabs-io/gouroboros/ledger"
	"github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/gouroboros/ledger/conway"
	ocommon "github.com/blinklabs-io/gouroboros/protocol/common"

	"github.com/chrisguiney/cardano-continuum/internal/chain"
	"github.com/chrisguiney/cardano-continuum/internal/fakechain"
	"github.com/chrisguiney/cardano-continuum/internal/keys"
	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/rng"
)

var errClosed = errors.New("fake connection closed")

// fakeDialer serves a fakechain.Chain as chain-sync. When a connection is
// caught up it runs the next of idle, so a test scripts what the chain does
// between reads; with none left it waits for cancellation.
type fakeDialer struct {
	t     *testing.T
	chain *fakechain.Chain
	// closeAfter[i] ends connection i after that many events (0: never).
	closeAfter []int
	// echo makes each connection answer its first request with a rollback
	// to the intersection, as a real node does.
	echo bool

	mu    sync.Mutex
	idle  []func()
	dials int
}

func (d *fakeDialer) Dial(context.Context) (Source, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	s := &fakeSource{d: d, echo: d.echo}
	if d.dials < len(d.closeAfter) {
		s.left = d.closeAfter[d.dials]
	}
	d.dials++
	return s, nil
}

func (d *fakeDialer) nextIdle() func() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.idle) == 0 {
		return nil
	}
	fn := d.idle[0]
	d.idle = d.idle[1:]
	return fn
}

type fakeSource struct {
	d    *fakeDialer
	f    *fakechain.Follower
	left int // events before the connection drops; 0 means never
	echo bool
	at   chain.Point
}

func (s *fakeSource) Intersect(_ context.Context, points []chain.Point) (chain.Point, error) {
	wire := make([]ocommon.Point, len(points))
	for i, p := range points {
		wire[i] = p.Wire()
	}
	f, at, err := s.d.chain.FollowFrom(wire, 0)
	if errors.Is(err, fakechain.ErrNoIntersect) {
		return chain.Point{}, ErrNoIntersect
	}
	if err != nil {
		return chain.Point{}, err
	}
	s.f = f
	s.at, _ = chain.PointFromWire(at)
	return s.at, nil
}

func (s *fakeSource) Next(ctx context.Context) (chain.Event, error) {
	if s.left == 1 {
		return chain.Event{}, errClosed
	}
	if s.left > 1 {
		s.left--
	}
	if s.echo {
		s.echo = false
		return chain.Event{Kind: chain.Backward, Point: s.at}, nil
	}
	for {
		ev, ok := s.f.Next()
		if ok {
			return s.convert(ev)
		}
		fn := s.d.nextIdle()
		if fn == nil {
			<-ctx.Done()
			return chain.Event{}, ctx.Err()
		}
		fn()
	}
}

func (s *fakeSource) convert(ev fakechain.Event) (chain.Event, error) {
	tip, _ := chain.PointFromWire(ev.Tip)
	if ev.Kind == fakechain.RollBackward {
		p, _ := chain.PointFromWire(ev.Point)
		return chain.Event{Kind: chain.Backward, Point: p, Tip: tip}, nil
	}
	lb, err := ledger.NewBlockFromCbor(ledger.BlockTypeConway, ev.Block.Cbor)
	if err != nil {
		return chain.Event{}, err
	}
	b := chain.BlockFrom(lb)
	return chain.Event{Kind: chain.Forward, Block: b, Point: b.Point, Tip: tip}, nil
}

func (s *fakeSource) Close() error { return nil }

type world struct {
	t     *testing.T
	store *keys.Store
	chain *fakechain.Chain
	alice keys.Wallet
	bob   keys.Wallet
}

// newWorld returns a fake chain with k = 3 and a block every 2 slots, where
// alice holds one genesis output.
func newWorld(t *testing.T) *world {
	t.Helper()
	store, err := keys.New(rng.Seed{3}, 0)
	if err != nil {
		t.Fatal(err)
	}
	w := &world{t: t, store: store, alice: store.Wallet(1), bob: store.Wallet(2)}
	w.chain, err = fakechain.New(fakechain.Config{
		Slots: testSlots(t), K: 3, BlockEvery: 2,
		Genesis: []fakechain.GenesisOutput{{Address: w.alice.Address(), Lovelace: 100_000_000}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func (w *world) book() *chain.Book {
	b := chain.NewBook()
	for id, wl := range map[population.ActorID]keys.Wallet{1: w.alice, 2: w.bob} {
		if err := b.AddAddress(wl.Address(), id); err != nil {
			w.t.Fatal(err)
		}
	}
	return b
}

func (w *world) genesis() []common.Utxo {
	b, err := w.alice.Address().Bytes()
	if err != nil {
		w.t.Fatal(err)
	}
	return []common.Utxo{utxo(common.Blake2b256Hash(b), 0, w.alice.Address(), 100_000_000)}
}

func (w *world) view(s chain.State) *chain.View {
	v, err := chain.New(chain.Config{K: 3, Slots: testSlots(w.t), Book: w.book()}, s)
	if err != nil {
		w.t.Fatal(err)
	}
	return v
}

func (w *world) advance(blocks int) func() {
	return func() {
		if err := w.chain.AdvanceTo(w.chain.Slot() + 2*uint64(blocks)); err != nil {
			w.t.Error(err)
		}
	}
}

func (w *world) rollback(depth uint64) func() {
	return func() {
		if err := w.chain.Inject(fakechain.Rollback{Depth: depth}); err != nil {
			w.t.Error(err)
		}
	}
}

// payBob submits alice's genesis output to bob, minus the minimum fee.
func (w *world) payBob() func() {
	return func() {
		g := w.genesis()[0]
		in := chain.RefOf(g.Id)
		bobAddr, err := w.bob.Address().Bytes()
		if err != nil {
			w.t.Fatal(err)
		}
		enc := func(fee uint64) []byte {
			body, err := cbor.Encode(map[uint]any{
				0: []any{[]any{in.Tx[:], in.Index}},
				1: []any{map[uint]any{0: bobAddr, 1: 100_000_000 - fee}},
				2: fee,
			})
			if err != nil {
				w.t.Fatal(err)
			}
			sig := w.alice.SignTxBody(body)
			tx, err := cbor.Encode([]any{cbor.RawMessage(body), map[uint]any{0: []any{[]any{sig.VKey, sig.Signature}}}, true, nil})
			if err != nil {
				w.t.Fatal(err)
			}
			return tx
		}
		probe, err := conway.NewConwayTransactionFromCbor(enc(1_000_000))
		if err != nil {
			w.t.Fatal(err)
		}
		pp := fakechain.DefaultParams()
		fee, err := conway.MinFeeTx(probe, &pp)
		if err != nil {
			w.t.Fatal(err)
		}
		if err := w.chain.Submit(context.Background(), fakechain.EraConway, enc(fee)); err != nil {
			w.t.Error(err)
		}
	}
}

// follow runs a follower until it has delivered n events, then stops it
// and returns the events and Run's error.
func follow(t *testing.T, d Dialer, start []chain.Point, n int) ([]chain.Event, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out := make(chan chain.Event)
	f := NewFollower(d, FollowerConfig{K: 3, Start: start, Backoff: func(int) time.Duration { return 0 }}, out, nil)
	done := make(chan error, 1)
	go func() { done <- f.Run(ctx) }()
	var got []chain.Event
	for len(got) < n {
		select {
		case ev := <-out:
			got = append(got, ev)
		case err := <-done:
			return got, err
		case <-ctx.Done():
			t.Fatalf("timed out after %d of %d events", len(got), n)
		}
	}
	cancel()
	return got, <-done
}

// drive feeds events to v the way the engine does: an intersection answer
// is a rollback.
func drive(t *testing.T, v *chain.View, evs []chain.Event) {
	t.Helper()
	for _, ev := range evs {
		var err error
		switch ev.Kind {
		case chain.Forward:
			_, err = v.Apply(ev.Block)
		case chain.Backward, chain.Intersected:
			_, err = v.Rollback(ev.Point)
		}
		if err != nil {
			t.Fatalf("%v at slot %d: %v", ev.Kind, ev.Point.Slot, err)
		}
	}
}

func TestFollowerDeliversInOrder(t *testing.T) {
	w := newWorld(t)
	d := &fakeDialer{t: t, chain: w.chain}
	d.idle = []func(){w.advance(3), w.rollback(1), w.advance(2)}

	got, err := follow(t, d, nil, 7)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v, want context.Canceled", err)
	}
	want := []chain.EventKind{chain.Intersected, chain.Forward, chain.Forward, chain.Forward, chain.Backward, chain.Forward, chain.Forward}
	var kinds []chain.EventKind
	for _, ev := range got {
		kinds = append(kinds, ev.Kind)
	}
	if !reflect.DeepEqual(kinds, want) {
		t.Fatalf("events %v, want %v", kinds, want)
	}
	if !got[0].Point.IsOrigin() || got[4].Point != got[2].Point {
		t.Errorf("intersect at %v, rollback to %v (want origin, block 2)", got[0].Point, got[4].Point)
	}
	v := w.view(chain.State{})
	drive(t, v, got)
	if tip, _ := chain.PointFromWire(w.chain.Tip()); v.Tip() != tip {
		t.Errorf("view tip %v, chain tip %v", v.Tip(), tip)
	}
}

func TestFollowerFeedsView(t *testing.T) {
	w := newWorld(t)
	d := &fakeDialer{t: t, chain: w.chain}
	d.idle = []func(){w.payBob(), w.advance(1)}
	got, _ := follow(t, d, nil, 2)
	v := w.view(chain.State{Utxos: w.genesis()})
	drive(t, v, got)
	if gotB, want := v.UtxosAt(w.bob.Address()), w.chain.UtxosAt(w.bob.Address()); len(want) != 1 ||
		len(gotB) != 1 || chain.RefOf(gotB[0].Id) != chain.RefOf(want[0].Id) {
		t.Errorf("view has %v at bob, chain has %v", gotB, want)
	}
	if len(v.UtxosAt(w.alice.Address())) != 0 {
		t.Error("alice's spent genesis output is still in the view")
	}
}

func TestFollowerReconnects(t *testing.T) {
	w := newWorld(t)
	d := &fakeDialer{t: t, chain: w.chain, closeAfter: []int{5}, echo: true}
	d.idle = []func(){w.advance(5)}

	got, err := follow(t, d, nil, 7)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v", err)
	}
	if d.dials != 2 {
		t.Errorf("dialled %d times, want 2", d.dials)
	}
	seen := map[chain.Point]bool{}
	var forwards int
	for _, ev := range got {
		if ev.Kind != chain.Forward {
			continue
		}
		if seen[ev.Point] {
			t.Errorf("block at slot %d delivered twice", ev.Point.Slot)
		}
		seen[ev.Point] = true
		forwards++
	}
	if forwards != 5 {
		t.Errorf("delivered %d blocks, want 5", forwards)
	}
	for _, ev := range got {
		if ev.Kind == chain.Backward {
			t.Errorf("the node's rollback to the intersection was passed on (slot %d)", ev.Point.Slot)
		}
	}
	// The reconnect intersects at the last block delivered, which is the
	// view's tip, so the engine's rollback to it is a no-op.
	v := w.view(chain.State{})
	drive(t, v, got)
	if tip, _ := chain.PointFromWire(w.chain.Tip()); v.Tip() != tip {
		t.Errorf("view tip %v, chain tip %v", v.Tip(), tip)
	}
}

func TestResumeFromSettledPoint(t *testing.T) {
	w := newWorld(t)
	d := &fakeDialer{t: t, chain: w.chain}
	d.idle = []func(){w.payBob(), w.advance(6)}
	first, _ := follow(t, d, nil, 7)
	live := w.view(chain.State{Utxos: w.genesis()})
	drive(t, live, first)
	base := live.Base()
	if base.At.IsOrigin() {
		t.Fatal("nothing settled")
	}

	// Restart: a view from the settled state and a follower whose only
	// stored point is the settled point. While it was down the node rolled
	// back two blocks and made three more.
	w.rollback(2)()
	w.advance(3)()
	d2 := &fakeDialer{t: t, chain: w.chain}
	second, _ := follow(t, d2, []chain.Point{base.At}, 1+4)
	if second[0].Kind != chain.Intersected || second[0].Point != base.At {
		t.Fatalf("first event %v at %v, want chain.Intersected at the settled point %v", second[0].Kind, second[0].Point, base.At)
	}
	restarted := w.view(base)
	drive(t, restarted, second)

	var chainBlocks []chain.Block
	for _, b := range w.chain.Blocks() {
		lb, err := ledger.NewBlockFromCbor(ledger.BlockTypeConway, b.Cbor)
		if err != nil {
			t.Fatal(err)
		}
		chainBlocks = append(chainBlocks, chain.BlockFrom(lb))
	}
	fresh := w.view(chain.State{Utxos: w.genesis()})
	for _, b := range chainBlocks {
		mustApply(t, fresh, b)
	}
	if !reflect.DeepEqual(restarted.Snapshot(), fresh.Snapshot()) {
		t.Errorf("restarted view\n got %+v\nwant %+v", restarted.Snapshot(), fresh.Snapshot())
	}
	if second[1].Block.Number != base.Height+1 {
		t.Errorf("replay starts at block %d, want %d (no gap)", second[1].Block.Number, base.Height+1)
	}
}

func TestRestartAfterOfflineRollback(t *testing.T) {
	w := newWorld(t)
	d := &fakeDialer{t: t, chain: w.chain}
	d.idle = []func(){w.advance(6)}
	first, _ := follow(t, d, nil, 7)
	live := w.view(chain.State{})
	drive(t, live, first)

	// Stored points reach from the tip down to the settled point.
	var stored []chain.Point
	for _, ev := range first[1:] {
		if heightOfPoint(first, ev.Point) >= heightOfPoint(first, live.Settled()) {
			stored = append(stored, ev.Point)
		}
	}
	w.rollback(2)()
	w.advance(1)()
	second, _ := follow(t, &fakeDialer{t: t, chain: w.chain}, stored, 2)
	if second[0].Kind != chain.Intersected || second[0].Point != first[4].Point {
		t.Fatalf("intersected at %v, want block 4 (below the stored tip)", second[0].Point)
	}
	drive(t, live, second)
	if tip, _ := chain.PointFromWire(w.chain.Tip()); live.Tip() != tip {
		t.Errorf("view tip %v, chain tip %v", live.Tip(), tip)
	}
}

func heightOfPoint(evs []chain.Event, p chain.Point) int {
	for i, ev := range evs {
		if ev.Point == p {
			return i
		}
	}
	return -1
}

func TestFollowerNoIntersectIsFatal(t *testing.T) {
	w := newWorld(t)
	d := &fakeDialer{t: t, chain: w.chain}
	_, err := follow(t, d, []chain.Point{{Slot: 9, Hash: txid(9)}}, 1)
	if !errors.Is(err, ErrNoIntersect) {
		t.Fatalf("Run = %v, want ErrNoIntersect", err)
	}
	if d.dials != 1 {
		t.Errorf("dialled %d times; a missing intersection must not be retried", d.dials)
	}
}
