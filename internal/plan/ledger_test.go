package plan

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"testing"

	"github.com/chrisguiney/cardano-continuum/internal/chain"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/store"
)

const farTTL = 1_000_000

func wantRefs(t *testing.T, what string, got, want []Ref) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Fatalf("%s = %v, want %v", what, got, want)
	}
}

func wantTxs(t *testing.T, what string, got []TxID, want ...TxID) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Fatalf("%s = %v, want %v", what, got, want)
	}
}

func unfinishedTxs(l *Ledger) []TxID {
	var out []TxID
	for _, p := range l.Unfinished() {
		out = append(out, p.Tx)
	}
	return out
}

func TestAvailableIsOwnSettledOutputs(t *testing.T) {
	w := newWorld(t)
	wantRefs(t, "actor 1", w.avail(1, 1), []Ref{g1, g2})
	wantRefs(t, "actor 2", w.avail(2, 2), []Ref{g3})
}

func TestReserveExcludes(t *testing.T) {
	w := newWorld(t)
	w.reserve(1, w.attempt(1, 1, farTTL, g1))
	wantRefs(t, "another intent", w.avail(1, 2), []Ref{ref(1), g2})
	wantRefs(t, "the holder", w.avail(1, 1), []Ref{ref(1), g1, g2})
	if err := w.l.Reserve(context.Background(), 2, w.attempt(2, 1, farTTL, g1)); !errors.Is(err, ErrNotAvailable) {
		t.Fatalf("double reserve: %v", err)
	}
	if held(w.l, g1) != 1 {
		t.Fatal("the refused reserve changed the holder")
	}
}

func TestChainingOffersPendingChange(t *testing.T) {
	w := newWorld(t)
	w.reserve(1, w.attempt(1, 1, farTTL, g1))
	wantRefs(t, "with Chain", w.avail(1, 2), []Ref{ref(1), g2})
	wantRefs(t, "without Chain", refsOf(w.l.Available(1, 2, Opts{})), []Ref{g2})
}

func TestChainDepthBound(t *testing.T) {
	w := newWorld(t)
	w.reserve(1, w.attempt(1, 1, farTTL, g1))
	w.reserve(2, w.attempt(2, 1, farTTL, ref(1)))
	wantRefs(t, "depth 2", refsOf(w.l.Available(1, 3, Opts{Chain: true, MaxDepth: 2})), []Ref{g2})
	wantRefs(t, "depth 3", refsOf(w.l.Available(1, 3, Opts{Chain: true, MaxDepth: 3})), []Ref{ref(2), g2})
}

func TestDropNoticeReleasesNothing(t *testing.T) {
	w := newWorld(t)
	w.reserve(1, w.attempt(1, 1, farTTL, g1))
	before := w.avail(1, 2)
	w.l.DropNotice(txid(1))
	if !w.l.Dropped(txid(1)) {
		t.Fatal("notice not recorded")
	}
	wantRefs(t, "after the notice", w.avail(1, 2), before)
	if held(w.l, g1) != 1 {
		t.Fatal("drop notice released g1")
	}
	wantTxs(t, "unfinished", unfinishedTxs(w.l), txid(1))
}

func TestReserveRefusesFreshInputsForSecondAttempt(t *testing.T) {
	w := newWorld(t)
	w.reserve(1, w.attempt(1, 1, farTTL, g1))
	w.l.DropNotice(txid(1))
	err := w.l.Reserve(context.Background(), 1, w.attempt(2, 1, farTTL, g2))
	if !errors.Is(err, ErrNotExclusive) {
		t.Fatalf("fresh inputs: %v", err)
	}
	if held(w.l, g2) != 0 {
		t.Fatal("refused attempt reserved g2")
	}
	w.reserve(1, w.attempt(2, 1, farTTL, g1, g2))
}

// The PlannerConflictWithLatestOnlyBug order: A spends g1; B, a second
// attempt, spends g1 too; C spends A's change. C conflicts with B through A
// but can land beside A, so it pays the intent twice.
func TestSecondAttemptExcludesEveryEarlierAttempt(t *testing.T) {
	w := newWorld(t)
	w.reserve(1, w.attempt(1, 1, farTTL, g1))
	w.reserve(1, w.attempt(2, 1, farTTL, g1))
	err := w.l.Reserve(context.Background(), 1, w.attempt(3, 1, farTTL, ref(1)))
	if !errors.Is(err, ErrNotExclusive) {
		t.Fatalf("third attempt on the first's change: %v", err)
	}
}

func TestSecondAttemptOnReplacedParent(t *testing.T) {
	w := newWorld(t)
	w.reserve(1, w.attempt(1, 1, farTTL, g1)) // parent P
	w.reserve(2, w.attempt(2, 1, farTTL, ref(1)))
	w.reserve(1, w.attempt(3, 1, farTTL, g1)) // P', replacing P
	// The child's second attempt spends P's replacement: it shares no input
	// with the child's first attempt, but builds on P', which excludes P.
	w.reserve(2, w.attempt(4, 1, farTTL, ref(3)))
}

func TestReserveRefusesContradictoryInputs(t *testing.T) {
	w := newWorld(t)
	w.reserve(1, w.attempt(1, 1, farTTL, g1))
	w.reserve(1, w.attempt(2, 1, farTTL, g1))
	err := w.l.Reserve(context.Background(), 2, w.attempt(3, 1, farTTL, ref(1), ref(2)))
	if !errors.Is(err, ErrContradictory) {
		t.Fatalf("built on both attempts: %v", err)
	}
}

func TestReserveRefusesMalformed(t *testing.T) {
	w := newWorld(t)
	w.reserve(1, w.attempt(1, 1, farTTL, g1))
	cases := map[string]Attempt{
		"no inputs":      w.attempt(2, 1, farTTL),
		"repeated input": w.attempt(2, 1, farTTL, g2, g2),
		"known tx":       w.attempt(1, 1, farTTL, g2),
		"foreign change": {Tx: txid(2), Actor: 1, Inputs: []Ref{g2}, Change: []Utxo{{Ref: ref(2), Owner: 2}}},
		"change of other tx": {Tx: txid(2), Actor: 1, Inputs: []Ref{g2},
			Change: []Utxo{{Ref: ref(9), Owner: 1}}},
	}
	for name, a := range cases {
		if err := w.l.Reserve(context.Background(), 2, a); !errors.Is(err, ErrBadAttempt) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := w.l.Reserve(context.Background(), 2, w.attempt(2, 1, farTTL, g3)); !errors.Is(err, ErrNotAvailable) {
		t.Errorf("another actor's output: %v", err)
	}
}

func TestConfirmedNotSettledKeepsReservation(t *testing.T) {
	w := newWorld(t)
	a := w.attempt(1, 1, farTTL, g1)
	w.reserve(1, a)
	for i := range testK {
		txs := []chain.Tx{}
		if i == 0 {
			txs = append(txs, w.txOf(a))
		}
		if res := w.block(uint64(10*(i+1)), txs...); len(res.Settled)+len(res.Dead) != 0 {
			t.Fatalf("block %d finished %+v", i, res)
		}
	}
	if held(w.l, g1) != 1 {
		t.Fatal("confirmed short of K released g1")
	}
	wantTxs(t, "unfinished", unfinishedTxs(w.l), txid(1))
	if err := w.l.Reserve(context.Background(), 2, w.attempt(2, 1, farTTL, g1)); !errors.Is(err, ErrNotAvailable) {
		t.Fatalf("g1 offered to another intent: %v", err)
	}
}

// The PlannerSettleAtTipBug order: the parent is confirmed, its child spends
// its change, and the block is rolled back.
func TestRollbackKeepsReservationAndDescendants(t *testing.T) {
	w := newWorld(t)
	p := w.attempt(1, 1, farTTL, g1)
	w.reserve(1, p)
	w.block(10, w.txOf(p))
	w.reserve(2, w.attempt(2, 1, farTTL, ref(1)))
	w.back(1)
	wantTxs(t, "unfinished", unfinishedTxs(w.l), txid(1), txid(2))
	if held(w.l, g1) != 1 || held(w.l, ref(1)) != 2 {
		t.Fatal("rollback released a reservation")
	}
	wantRefs(t, "offered to intent 3", w.avail(1, 3), []Ref{ref(2), g2})
}

func TestSettleReleases(t *testing.T) {
	w := newWorld(t)
	a := w.attempt(1, 1, farTTL, g1)
	w.reserve(1, a)
	w.block(10, w.txOf(a))
	w.block(20)
	res := w.block(30)
	wantTxs(t, "settled", res.Settled, txid(1))
	if _, ok := w.l.Holder(g1); ok {
		t.Fatal("g1 still held")
	}
	wantRefs(t, "available", w.avail(1, 2), []Ref{ref(1), g2})
	if err := w.l.Reserve(context.Background(), 1, w.attempt(2, 1, farTTL, g2)); !errors.Is(err, ErrPaid) {
		t.Fatalf("attempt on a paid intent: %v", err)
	}
	if w.l.ResumePoint() != w.points[0] {
		t.Fatalf("resume point %v, want %v", w.l.ResumePoint(), w.points[0])
	}
}

func TestDeadOnlyOnSettledConflict(t *testing.T) {
	w := newWorld(t)
	w.reserve(1, w.attempt(1, 1, farTTL, g1, g2))
	w.block(10, foreign(500, g1))
	if res := w.block(20); len(res.Dead) != 0 {
		t.Fatalf("dead before K deep: %+v", res)
	}
	if held(w.l, g2) != 1 {
		t.Fatal("released before K deep")
	}
	res := w.block(30)
	wantTxs(t, "dead", res.Dead, txid(1))
	if _, ok := w.l.Holder(g2); ok {
		t.Fatal("g2 still held by a dead attempt")
	}
	wantRefs(t, "available", w.avail(1, 2), []Ref{g2})
}

// The PlannerExpireAtTipBug order: the tip passing the TTL changes nothing;
// the settled tip reaching it does.
func TestDeadOnlyOnSettledExpiry(t *testing.T) {
	w := newWorld(t)
	w.reserve(1, w.attempt(1, 1, 30, g1))
	for _, slot := range []uint64{10, 20, 30, 40} {
		if res := w.block(slot); len(res.Dead) != 0 {
			t.Fatalf("dead at tip slot %d, settled slot %d", slot, w.l.ResumePoint().Slot)
		}
	}
	res := w.block(50) // settles slot 30
	wantTxs(t, "dead", res.Dead, txid(1))
}

// The PlannerNoCascadeBug order: the parent is refused; the child, which
// spends its change and g2, dies with it and g2 is free again.
func TestDeathCascadesToDescendants(t *testing.T) {
	w := newWorld(t)
	w.reserve(1, w.attempt(1, 1, farTTL, g1))
	w.reserve(2, w.attempt(2, 1, farTTL, ref(1), g2))
	dead, err := w.l.Refused(context.Background(), txid(1))
	if err != nil {
		t.Fatal(err)
	}
	wantTxs(t, "dead", dead, txid(1), txid(2))
	wantRefs(t, "available", w.avail(1, 3), []Ref{g1, g2})
	if len(w.l.Unfinished()) != 0 {
		t.Fatal("attempts left unfinished")
	}
	if _, err := w.l.Refused(context.Background(), txid(1)); !errors.Is(err, ErrUnknownTx) {
		t.Fatalf("second refusal: %v", err)
	}
}

func TestSettledSiblingKillsOtherAttempts(t *testing.T) {
	w := newWorld(t)
	a := w.attempt(1, 1, farTTL, g1)
	w.reserve(1, a)
	w.reserve(1, w.attempt(2, 1, farTTL, g1, g2))
	w.reserve(2, w.attempt(3, 1, farTTL, ref(2)))
	w.block(10, w.txOf(a))
	w.block(20)
	res := w.block(30)
	wantTxs(t, "settled", res.Settled, txid(1))
	wantTxs(t, "dead", res.Dead, txid(2), txid(3))
	wantRefs(t, "available", w.avail(1, 4), []Ref{ref(1), g2})
}

func TestReserveIsDurableBeforeReturn(t *testing.T) {
	w := newWorld(t)
	w.disk.fail = true
	if err := w.l.Reserve(context.Background(), 1, w.attempt(1, 1, farTTL, g1)); !errors.Is(err, errDisk) {
		t.Fatalf("reserve on a failing disk: %v", err)
	}
	if _, ok := w.l.Holder(g1); ok || len(w.l.Unfinished()) != 0 {
		t.Fatal("a failed commit reserved")
	}
	wantRefs(t, "available", w.avail(1, 2), []Ref{g1, g2})

	w.disk.fail = false
	w.reserve(1, w.attempt(1, 1, farTTL, g1))
	got, err := Open(w.cfg(), w.disk.last)
	if err != nil {
		t.Fatal(err)
	}
	if held(got, g1) != 1 {
		t.Fatal("the committed blob lacks the reservation")
	}
}

func TestAdvanceIsDurableOrUnchanged(t *testing.T) {
	w := newWorld(t)
	a := w.attempt(1, 1, farTTL, g1)
	w.reserve(1, a)
	w.block(10, w.txOf(a))
	w.block(20)
	w.disk.fail = true
	p := chain.Point{Slot: 30, Hash: txid(1_000_030)}
	_, err := w.l.Advance(context.Background(), chain.Event{Kind: chain.Forward, Point: p,
		Block: chain.Block{Point: p, Prev: w.tip().Hash}})
	if !errors.Is(err, errDisk) {
		t.Fatalf("advance on a failing disk: %v", err)
	}
	if held(w.l, g1) != 1 || !w.l.ResumePoint().IsOrigin() {
		t.Fatal("a failed commit settled")
	}
	w.disk.fail = false
	if res := w.block(30); !slices.Equal(res.Settled, []TxID{txid(1)}) {
		t.Fatalf("redelivered block: %+v", res)
	}
}

// The PlannerPersistLateBug order: a restart with an attempt in flight.
func TestRestartKeepsUnfinishedReservations(t *testing.T) {
	w := newWorld(t)
	a := w.attempt(1, 1, farTTL, g1)
	w.reserve(1, a)
	w.reserve(2, w.attempt(2, 1, farTTL, ref(1)))
	w.block(10, w.txOf(a))
	w.reopen()
	wantTxs(t, "unfinished", unfinishedTxs(w.l), txid(1), txid(2))
	wantRefs(t, "offered to intent 3", w.avail(1, 3), []Ref{ref(2), g2})
	if held(w.l, g1) != 1 || held(w.l, ref(1)) != 2 {
		t.Fatal("restart lost a reservation")
	}
	if w.l.Dropped(txid(1)) {
		t.Fatal("drop notices survive a restart")
	}
}

// The PlannerRestartFromOriginBug and PlannerSettledPointFollowsTipBug
// orders: after a rollback and a restart, settled spends stay spent.
func TestRestartDoesNotOfferSettledInputs(t *testing.T) {
	w := newWorld(t)
	a := w.attempt(1, 1, farTTL, g1)
	w.reserve(1, a)
	w.block(10, w.txOf(a))
	w.block(20)
	w.block(30) // settles slot 10
	settled := w.l.ResumePoint()
	w.back(1)
	if w.l.ResumePoint() != settled {
		t.Fatal("a rollback moved the settled point back")
	}
	w.reopen()
	if w.l.ResumePoint() != settled {
		t.Fatalf("resume point %v, want %v", w.l.ResumePoint(), settled)
	}
	wantRefs(t, "available", w.avail(1, 2), []Ref{ref(1), g2})
	if _, err := w.l.Advance(context.Background(), chain.Event{Kind: chain.Backward}); !errors.Is(err, ErrTooDeep) {
		t.Fatalf("rollback to the origin: %v", err)
	}
	// chain-sync resumes at the settled point and replays from there.
	w.points = w.points[:1]
	w.block(20)
}

func TestBlockMustConnect(t *testing.T) {
	w := newWorld(t)
	w.block(10)
	p := chain.Point{Slot: 20, Hash: txid(9)}
	_, err := w.l.Advance(context.Background(), chain.Event{Kind: chain.Forward, Point: p,
		Block: chain.Block{Point: p, Prev: txid(8)}})
	if !errors.Is(err, ErrNotConnected) {
		t.Fatalf("disconnected block: %v", err)
	}
}

func TestBlobRoundTripsThroughStore(t *testing.T) {
	w := newWorld(t)
	a := w.attempt(1, 1, 500, g1)
	w.reserve(1, a)
	w.reserve(2, w.attempt(2, 1, farTTL, ref(1), g2))
	w.block(10, w.txOf(a))
	w.block(20)
	w.block(30)
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if err := db.SaveCheckpoint(ctx, w.checkpoint()); err != nil {
		t.Fatal(err)
	}
	c, err := db.LoadCheckpoint(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if c.Reservations.Version != BlobVersion {
		t.Fatalf("version %d", c.Reservations.Version)
	}
	got, err := Open(w.cfg(), c.Reservations)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.EqualFunc(got.Unfinished(), w.l.Unfinished(), pendingEqual) {
		t.Fatalf("unfinished %+v, want %+v", got.Unfinished(), w.l.Unfinished())
	}
	if got.ResumePoint() != w.l.ResumePoint() {
		t.Fatal("resume point differs")
	}
	wantRefs(t, "available", refsOf(got.Available(1, 3, Opts{Chain: true})), w.avail(1, 3))
	if err := db.SaveCheckpoint(ctx, w.checkpoint()); err != nil {
		t.Fatal(err)
	}
}

func (w *world) checkpoint() store.Checkpoint {
	p := w.l.ResumePoint().Log()
	return store.Checkpoint{Confirmed: p, Points: []eventlog.ChainPoint{p}, Reservations: w.disk.last}
}

func pendingEqual(a, b Pending) bool {
	return a.Intent == b.Intent && a.Tx == b.Tx && a.Actor == b.Actor && a.TTL == b.TTL &&
		slices.Equal(a.Inputs, b.Inputs) && slices.Equal(a.Change, b.Change)
}

func TestOpenRefusesBadBlobs(t *testing.T) {
	w := newWorld(t)
	w.reserve(1, w.attempt(1, 1, farTTL, g1))
	good := w.disk.last
	for name, b := range map[string]store.Blob{
		"old version": {Version: 1, Data: good.Data},
		"garbage":     {Version: BlobVersion, Data: []byte{0xff, 0x00}},
		"empty":       {Version: BlobVersion},
	} {
		if _, err := Open(w.cfg(), b); !errors.Is(err, ErrBlob) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// TestSettledListsOutputsAtTheSettledPoint: Settled is every owned output
// unspent at the settled point, reserved or not, and ignores blocks above it.
func TestSettledListsOutputsAtTheSettledPoint(t *testing.T) {
	w := newWorld(t)
	a := w.attempt(1, 1, farTTL, g1)
	w.reserve(1, a)
	wantRefs(t, "settled before", refsOf(w.l.Settled()), []Ref{g1, g2, g3})
	w.block(10, w.txOf(a))
	w.block(20)
	wantRefs(t, "settled, block 10 not K deep", refsOf(w.l.Settled()), []Ref{g1, g2, g3})
	w.block(30)
	wantRefs(t, "settled, block 10 K deep", refsOf(w.l.Settled()), []Ref{ref(1), g2, g3})
}

// TestBlobIsWhatACommitWrites: Blob encodes the current state as Commit
// would, and Open of it gives a ledger with the same Blob.
func TestBlobIsWhatACommitWrites(t *testing.T) {
	w := newWorld(t)
	w.reserve(1, w.attempt(1, 1, farTTL, g1))
	b, err := w.l.Blob()
	if err != nil {
		t.Fatal(err)
	}
	if b.Version != w.disk.last.Version || !slices.Equal(b.Data, w.disk.last.Data) {
		t.Fatal("Blob differs from the last commit")
	}
	l, err := Open(w.cfg(), b)
	if err != nil {
		t.Fatal(err)
	}
	b2, err := l.Blob()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(b.Data, b2.Data) {
		t.Fatal("reopened ledger encodes differently")
	}
}
