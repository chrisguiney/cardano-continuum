package plan

import (
	"context"
	"encoding/binary"
	"errors"
	"slices"
	"testing"

	"github.com/blinklabs-io/gouroboros/ledger/babbage"
	"github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/gouroboros/ledger/mary"
	"github.com/blinklabs-io/gouroboros/ledger/shelley"

	"github.com/chrisguiney/cardano-continuum/internal/chain"
	"github.com/chrisguiney/cardano-continuum/internal/keys"
	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/rng"
	"github.com/chrisguiney/cardano-continuum/internal/store"
)

const testK = 2

var errDisk = errors.New("disk full")

// memDisk is a Committer holding the last committed blob, as a store would
// after a crash. fail makes the next commits fail.
type memDisk struct {
	last    store.Blob
	commits int
	fail    bool
}

func (d *memDisk) Commit(_ context.Context, b store.Blob) error {
	if d.fail {
		return errDisk
	}
	d.last = store.Blob{Version: b.Version, Data: slices.Clone(b.Data)}
	d.commits++
	return nil
}

// owners maps address bytes to actors.
type owners map[string]population.ActorID

func (o owners) Owner(a common.Address) (population.ActorID, bool) {
	b, err := a.Bytes()
	if err != nil {
		return 0, false
	}
	id, ok := o[string(b)]
	return id, ok
}

func txid(n uint64) TxID {
	var h TxID
	binary.BigEndian.PutUint64(h[24:], n)
	return h
}

func ref(n uint64) Ref { return Ref{Tx: txid(n), Index: 0} }

// world drives one ledger with hand-made blocks. Actors 1 and 2 exist;
// actor 1 owns genesis outputs g1 (10 ADA) and g2 (20 ADA), actor 2 owns g3.
type world struct {
	t      *testing.T
	ks     *keys.Store
	owners owners
	disk   *memDisk
	l      *Ledger
	points []chain.Point // the test chain's blocks, oldest first
}

var (
	g1 = ref(101)
	g2 = ref(102)
	g3 = ref(103)
)

func newWorld(t *testing.T) *world {
	t.Helper()
	ks, err := keys.New(rng.Seed{7}, 0)
	if err != nil {
		t.Fatal(err)
	}
	w := &world{t: t, ks: ks, owners: owners{}, disk: &memDisk{}}
	for _, a := range []population.ActorID{1, 2, 3} {
		b, err := w.addr(a).Bytes()
		if err != nil {
			t.Fatal(err)
		}
		w.owners[string(b)] = a
	}
	l, err := New(w.cfg(), []Utxo{
		{Ref: g1, Owner: 1, Lovelace: 10_000_000},
		{Ref: g2, Owner: 1, Lovelace: 20_000_000},
		{Ref: g3, Owner: 2, Lovelace: 30_000_000},
	})
	if err != nil {
		t.Fatal(err)
	}
	w.l = l
	return w
}

func (w *world) cfg() Config { return Config{K: testK, Owners: w.owners, Store: w.disk} }

func (w *world) addr(a population.ActorID) common.Address {
	return w.ks.Wallet(keys.ActorID(a)).Address()
}

// attempt is transaction n of actor a spending ins, with one change output
// (n, 0) of lovelace back to a.
func (w *world) attempt(n uint64, a population.ActorID, ttl uint64, ins ...Ref) Attempt {
	return Attempt{Tx: txid(n), Actor: a, Inputs: ins, TTL: ttl,
		Change: []Utxo{{Ref: ref(n), Owner: a, Lovelace: 1_000_000}}}
}

func (w *world) reserve(id IntentID, a Attempt) {
	w.t.Helper()
	if err := w.l.Reserve(context.Background(), id, a); err != nil {
		w.t.Fatalf("reserve %d: %v", id, err)
	}
}

// txOf is attempt a as a block would carry it.
func (w *world) txOf(a Attempt) chain.Tx {
	tx := chain.Tx{Hash: a.Tx, Spends: a.Inputs}
	for _, c := range a.Change {
		o := babbage.BabbageTransactionOutput{
			OutputAddress: w.addr(c.Owner),
			OutputAmount:  mary.MaryTransactionOutputValue{Amount: c.Lovelace},
		}
		tx.Creates = append(tx.Creates, common.Utxo{
			Id:     shelley.ShelleyTransactionInput{TxId: common.Blake2b256(c.Ref.Tx), OutputIndex: c.Ref.Index},
			Output: &o,
		})
	}
	return tx
}

// foreign is somebody else's transaction n spending ins.
func foreign(n uint64, ins ...Ref) chain.Tx { return chain.Tx{Hash: txid(n), Spends: ins} }

func (w *world) tip() chain.Point {
	if len(w.points) == 0 {
		return chain.Point{}
	}
	return w.points[len(w.points)-1]
}

// block extends the test chain at slot and feeds it to the ledger.
func (w *world) block(slot uint64, txs ...chain.Tx) Result {
	w.t.Helper()
	p := chain.Point{Slot: slot, Hash: txid(1_000_000 + slot)}
	b := chain.Block{Point: p, Prev: w.tip().Hash, Number: uint64(len(w.points) + 1), Txs: txs}
	w.points = append(w.points, p)
	res, err := w.l.Advance(context.Background(), chain.Event{Kind: chain.Forward, Block: b, Point: p})
	if err != nil {
		w.t.Fatalf("advance to slot %d: %v", slot, err)
	}
	return res
}

// back rolls the test chain and the ledger back depth blocks.
func (w *world) back(depth int) {
	w.t.Helper()
	w.points = w.points[:len(w.points)-depth]
	if _, err := w.l.Advance(context.Background(), chain.Event{Kind: chain.Backward, Point: w.tip()}); err != nil {
		w.t.Fatalf("rollback %d: %v", depth, err)
	}
}

// reopen restarts the ledger from what was committed.
func (w *world) reopen() {
	w.t.Helper()
	l, err := Open(w.cfg(), w.disk.last)
	if err != nil {
		w.t.Fatalf("open: %v", err)
	}
	w.l = l
}

func refsOf(us []Utxo) []Ref {
	out := make([]Ref, len(us))
	for i, u := range us {
		out[i] = u.Ref
	}
	return out
}

func (w *world) avail(a population.ActorID, id IntentID) []Ref {
	return refsOf(w.l.Available(a, id, Opts{Chain: true}))
}

func held(l *Ledger, r Ref) IntentID {
	id, _ := l.Holder(r)
	return id
}
