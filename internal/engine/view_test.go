package engine

import (
	"context"
	"testing"
	"time"

	"github.com/chrisguiney/cardano-continuum/internal/build"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/fakenode"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/fakechain"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

// fullParams are the fake chain's parameters in full: deposits, prices and
// the cost model, which certificates and scripts need.
func fullParams() build.Params {
	pp := fakechain.DefaultParams()
	p := build.ParamsFromConway(&pp, 0)
	p.SystemStart, p.SlotLength = time.UnixMilli(0).UTC(), time.Second
	return p
}

// feedBlocks hands the engine every fake-chain block after the n it has.
func feedBlocks(t *testing.T, e *Engine, c *fakechain.Chain, n int) int {
	t.Helper()
	bs := c.Blocks()
	for _, b := range bs[n:] {
		content, err := Digest(b.Cbor)
		if err != nil {
			t.Fatal(err)
		}
		body, err := eventlog.Encode(eventlog.BlockApplied{Point: fakenode.ChainPoint(b.Point()), Content: content})
		if err != nil {
			t.Fatal(err)
		}
		at := sched.Time(b.Slot) * 1000
		mustHandle(t, e, eventlog.Record{Class: eventlog.Input, Kind: eventlog.KindBlockApplied, At: at, Body: body})
	}
	return len(bs)
}

// TestViewFoldsCertificates: a stake registration in a fake-chain block
// reaches the agendas' view through the block digest, survives a snapshot
// and leaves with a rollback.
func TestViewFoldsCertificates(t *testing.T) {
	t.Parallel()
	cfg := testConfig(t, 1, 2)
	cfg.Build = fullParams()
	e, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	fc, err := NewFakeChain(&cfg)
	if err != nil {
		t.Fatal(err)
	}
	w := e.co.Rec(1).Wallet
	got, err := e.builder.Stake().RegisterStake(context.Background(), w, build.Spend{Inputs: fc.UtxosAt(w.Address()), TTL: 1000})
	if err != nil {
		t.Fatal(err)
	}
	n := feedBlocks(t, e, fc, 0)
	before := e.Tip()
	if err := fc.Submit(context.Background(), fakechain.EraConway, got.Tx); err != nil {
		t.Fatal(err)
	}
	if err := fc.AdvanceTo(fc.Slot() + cfg.BlockEvery); err != nil {
		t.Fatal(err)
	}
	feedBlocks(t, e, fc, n)
	if !viewOf(e).StakeRegistered(1) {
		t.Fatal("stake registration not in the view")
	}
	r, err := Restore(cfg, snap(t, e))
	if err != nil {
		t.Fatal(err)
	}
	if !viewOf(r).StakeRegistered(1) {
		t.Fatal("stake registration lost across a snapshot")
	}
	mustHandle(t, r, rollbackInput(t, before))
	if viewOf(r).StakeRegistered(1) {
		t.Fatal("stake registration survived the rollback")
	}
}
