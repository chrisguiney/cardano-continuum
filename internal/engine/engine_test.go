package engine

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"testing"

	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

func point(n uint64) eventlog.ChainPoint {
	return eventlog.ChainPoint{Slot: n * 10, Hash: binary.BigEndian.AppendUint64(make([]byte, 24), n)}
}

func blockInput(t *testing.T, n uint64, txs ...TxDigest) eventlog.Record {
	t.Helper()
	content, err := eventlog.Encode(BlockContent{Txs: txs})
	if err != nil {
		t.Fatal(err)
	}
	body, err := eventlog.Encode(eventlog.BlockApplied{Point: point(n), Content: content})
	if err != nil {
		t.Fatal(err)
	}
	return eventlog.Record{Class: eventlog.Input, Kind: eventlog.KindBlockApplied, At: sched.Time(n * 10000), Body: body}
}

func rollbackInput(t *testing.T, to eventlog.ChainPoint) eventlog.Record {
	t.Helper()
	body, err := eventlog.Encode(eventlog.RolledBack{To: to})
	if err != nil {
		t.Fatal(err)
	}
	return eventlog.Record{Class: eventlog.Input, Kind: eventlog.KindRolledBack, Body: body}
}

func mustHandle(t *testing.T, e *Engine, in eventlog.Record) []eventlog.Record {
	t.Helper()
	out, err := e.Handle(context.Background(), in)
	if err != nil {
		t.Fatalf("handle %s: %v", in.Kind, err)
	}
	return out
}

func snap(t *testing.T, e *Engine) []byte {
	t.Helper()
	b, err := e.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// moveTx spends actor 1's genesis output into an output to actor 2.
func moveTx(t *testing.T, cfg Config) TxDigest {
	t.Helper()
	funds, err := cfg.Genesis()
	if err != nil {
		t.Fatal(err)
	}
	from, _ := funds[0].Address.Bytes()
	to, _ := funds[1].Address.Bytes()
	return TxDigest{
		ID:      TxID{1},
		Inputs:  []Ref{{Tx: TxID(blake2b256(from))}},
		Outputs: []TxOut{{Address: to, Lovelace: funds[0].Lovelace - 200_000}},
		Fee:     200_000,
	}
}

func TestActorsChangeOnlyAtDepthK(t *testing.T) {
	t.Parallel()
	cfg := testConfig(t, 1, 2)
	cfg.K = 2
	e, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	before := e.Balances()
	mustHandle(t, e, blockInput(t, 1, moveTx(t, cfg)))
	mustHandle(t, e, blockInput(t, 2))
	if got := e.Balances(); got[1] != before[1] || got[2] != before[2] {
		t.Fatalf("block 1 changed durable balances with only 1 successor: %v -> %v", before, got)
	}
	// A rollback of the tip leaves durable actors unchanged.
	mustHandle(t, e, rollbackInput(t, point(1)))
	if got := e.Balances(); got[1] != before[1] || !samePoint(e.Confirmed(), eventlog.ChainPoint{}) {
		t.Fatalf("tip rollback changed durable state: %v, confirmed %v", got, e.Confirmed())
	}
	mustHandle(t, e, blockInput(t, 3))
	mustHandle(t, e, blockInput(t, 4))
	got := e.Balances()
	if got[1] != 0 || got[2] != before[2]+before[1]-200_000 {
		t.Fatalf("block 1 not durable after K successors: %v -> %v", before, got)
	}
	if !samePoint(e.Confirmed(), point(1)) {
		t.Fatalf("confirmed = %v, want block 1", e.Confirmed())
	}
	if e.Supply() != genesisTotal(t, cfg) {
		t.Fatalf("supply %d, genesis %d", e.Supply(), genesisTotal(t, cfg))
	}
}

func TestRollbackBelowConfirmedIsError(t *testing.T) {
	t.Parallel()
	cfg := testConfig(t, 1, 2)
	cfg.K = 2
	e, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for n := uint64(1); n <= 4; n++ {
		mustHandle(t, e, blockInput(t, n))
	}
	if !samePoint(e.Confirmed(), point(2)) {
		t.Fatalf("confirmed = %v, want block 2", e.Confirmed())
	}
	before := snap(t, e)
	for _, to := range []eventlog.ChainPoint{point(1), {}} {
		_, err := e.Handle(context.Background(), rollbackInput(t, to))
		if !errors.Is(err, ErrRollbackBelowConfirmed) {
			t.Fatalf("rollback to slot %d: err = %v, want ErrRollbackBelowConfirmed", to.Slot, err)
		}
		if !bytes.Equal(snap(t, e), before) {
			t.Fatal("a refused rollback changed the engine")
		}
	}
	if _, err := e.Handle(context.Background(), rollbackInput(t, eventlog.ChainPoint{Slot: 35, Hash: []byte{9}})); !errors.Is(err, ErrUnknownPoint) {
		t.Fatalf("rollback to an unknown point: err = %v", err)
	}
	mustHandle(t, e, rollbackInput(t, point(2)))
	if !samePoint(e.Tip(), point(2)) {
		t.Fatalf("rollback to the confirmed point: tip %v", e.Tip())
	}
}

func tickInput(at sched.Time) eventlog.Record {
	return eventlog.Record{Class: eventlog.Input, Kind: eventlog.KindTick, At: at}
}

// TestHandleIsPure: the same inputs give the same decisions and state, from a
// fresh engine or from a restored snapshot.
func TestHandleIsPure(t *testing.T) {
	t.Parallel()
	cfg := testConfig(t, 3, 8)
	inputs := []eventlog.Record{tickInput(1), tickInput(5_000), blockInput(t, 1, moveTx(t, cfg)), tickInput(20_000),
		blockInput(t, 2), blockInput(t, 3), tickInput(60_000), blockInput(t, 4), tickInput(90_000)}
	run := func(e *Engine, ins []eventlog.Record) [][]eventlog.Record {
		var out [][]eventlog.Record
		for _, in := range ins {
			out = append(out, mustHandle(t, e, in))
		}
		return out
	}
	a, _ := New(cfg)
	b, _ := New(cfg)
	da, db := run(a, inputs), run(b, inputs)
	decisions := 0
	for i := range da {
		decisions += len(da[i])
		if len(da[i]) != len(db[i]) {
			t.Fatalf("input %d: %d vs %d decisions", i, len(da[i]), len(db[i]))
		}
		for j := range da[i] {
			if !sameRecord(da[i][j], db[i][j]) {
				t.Fatalf("input %d decision %d differs: %s vs %s", i, j, da[i][j].Kind, db[i][j].Kind)
			}
		}
	}
	if decisions < 20 {
		t.Fatalf("only %d decisions; the fixture does not exercise the engine", decisions)
	}
	if !bytes.Equal(snap(t, a), snap(t, b)) {
		t.Fatal("snapshots differ")
	}

	// Split at every input: snapshot, restore, continue.
	for cut := range inputs {
		x, _ := New(cfg)
		run(x, inputs[:cut])
		y, err := Restore(cfg, snap(t, x))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(snap(t, y), snap(t, x)) {
			t.Fatalf("cut %d: restored snapshot differs", cut)
		}
		got := run(y, inputs[cut:])
		for i := range got {
			for j := range got[i] {
				if !sameRecord(got[i][j], da[cut+i][j]) {
					t.Fatalf("cut %d: input %d decision %d differs after restore", cut, cut+i, j)
				}
			}
		}
		if !bytes.Equal(snap(t, y), snap(t, a)) {
			t.Fatalf("cut %d: final state differs after restore", cut)
		}
	}
}

func sameRecord(a, b eventlog.Record) bool {
	return a.At == b.At && a.Kind == b.Kind && a.Actor == b.Actor && a.Class == b.Class && bytes.Equal(a.Body, b.Body)
}

func TestLargestFirst(t *testing.T) {
	t.Parallel()
	avail := []Coin{{Ref: Ref{Index: 0}, Lovelace: 5}, {Ref: Ref{Index: 1}, Lovelace: 9}, {Ref: Ref{Index: 2}, Lovelace: 9}, {Ref: Ref{Index: 3}, Lovelace: 1}}
	got := LargestFirst{}.Select(avail, 15)
	if len(got) != 2 || got[0].Ref.Index != 1 || got[1].Ref.Index != 2 {
		t.Fatalf("Select = %v, want the two 9s, lower ref first", got)
	}
	if (LargestFirst{}).Select(avail, 25) != nil {
		t.Fatal("Select covered a need above the total")
	}
}
