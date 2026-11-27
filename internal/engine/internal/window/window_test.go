package window

import (
	"encoding/binary"
	"errors"
	"testing"

	"github.com/blinklabs-io/gouroboros/ledger/common"

	"github.com/chrisguiney/cardano-continuum/internal/chain"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/wire"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

// nobody owns no address, so every output counts as foreign.
type nobody struct{}

func (nobody) Owner([]byte) (population.ActorID, bool)   { return 0, false }
func (nobody) Address(population.ActorID) common.Address { return common.Address{} }

func point(n uint64) eventlog.ChainPoint {
	return eventlog.ChainPoint{Slot: n * 10, Hash: binary.BigEndian.AppendUint64(make([]byte, 24), n)}
}

func open(t *testing.T, k uint64) *Window {
	t.Helper()
	slots, err := sched.NewSlots(1000, 100)
	if err != nil {
		t.Fatal(err)
	}
	w := New(chain.Config{K: k, Slots: slots})
	if err := w.Open(chain.State{}, Saved{}); err != nil {
		t.Fatal(err)
	}
	return w
}

func push(t *testing.T, w *Window, n uint64) {
	t.Helper()
	b := wire.Block{Point: point(n), Txs: []wire.TxDigest{{Fee: 1}}}
	_, vb, err := w.Blocks(b, nobody{})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Push(b, vb); err != nil {
		t.Fatal(err)
	}
	w.Confirm(nobody{})
}

// TestConfirmsOnlyAtDepthK: a block counts only once its Kth successor is
// pushed (StableOnly in models/specs/engine-recovery).
func TestConfirmsOnlyAtDepthK(t *testing.T) {
	t.Parallel()
	w := open(t, 2)
	for n := uint64(1); n <= 2; n++ {
		push(t, w, n)
		if got := w.Saved(); got.Conf.Slot != 0 || got.Fees != 0 {
			t.Fatalf("after block %d: saved %+v, want nothing confirmed", n, got)
		}
	}
	push(t, w, 3)
	if got := w.Saved(); got.Conf.Slot != point(1).Slot || got.Fees != 1 {
		t.Fatalf("after block 3: saved %+v, want block 1 confirmed with its fee", got)
	}
	if pts := w.Points(); len(pts) != 3 || pts[0].Slot != point(1).Slot || w.Tip().Slot != point(3).Slot {
		t.Fatalf("points %v, tip %v", pts, w.Tip())
	}
}

// TestRollbackBelowConfirmedIsRefused: Check refuses a point below the
// confirmed one and changes nothing (NeverHalted).
func TestRollbackBelowConfirmedIsRefused(t *testing.T) {
	t.Parallel()
	w := open(t, 1)
	for n := uint64(1); n <= 3; n++ {
		push(t, w, n)
	}
	before := w.Points()
	if _, err := w.Check(point(1)); !errors.Is(err, ErrRollbackBelowConfirmed) {
		t.Fatalf("rollback below confirmed: err = %v", err)
	}
	if _, err := w.Check(eventlog.ChainPoint{Slot: 35, Hash: []byte{9}}); !errors.Is(err, ErrUnknownPoint) {
		t.Fatalf("rollback to an unknown point: err = %v", err)
	}
	if after := w.Points(); len(after) != len(before) {
		t.Fatalf("a refused rollback changed the window: %v, was %v", after, before)
	}
	c, err := w.Check(point(2))
	if err != nil {
		t.Fatal(err)
	}
	gone, err := w.Rollback(c)
	if err != nil {
		t.Fatal(err)
	}
	if len(gone) != 1 || gone[0].Point.Slot != point(3).Slot || w.Tip().Slot != point(2).Slot {
		t.Fatalf("rollback to the confirmed point: gone %v, tip %v", gone, w.Tip())
	}
}

// A parameter change's predecessor and group survive the digest the
// event log keeps, so the view ratifies from a replayed log as it did live.
func TestDigestKeepsParameterChange(t *testing.T) {
	t.Parallel()
	prev := chain.GovID{Tx: chain.TxID{7}, Index: 1}
	want := chain.Proposal{
		ID:       chain.GovID{Tx: chain.TxID{9}},
		Type:     "parameter_change",
		Deposit:  100,
		Prev:     &prev,
		GovGroup: true,
	}
	raw, err := wire.DigestOf(chain.Block{Txs: []chain.Tx{{Hash: want.ID.Tx, Proposals: []chain.Proposal{want}}}})
	if err != nil {
		t.Fatal(err)
	}

	var c wire.BlockContent
	if err := eventlog.Decode(raw, &c); err != nil {
		t.Fatal(err)
	}

	tx, err := viewTx(c.Txs[0])
	if err != nil {
		t.Fatal(err)
	}

	got := tx.Proposals[0]
	if got.ID != want.ID || got.Type != want.Type || got.Prev == nil || *got.Prev != prev || !got.GovGroup {
		t.Fatalf("proposal %+v after the digest, want %+v", got, want)
	}
}
