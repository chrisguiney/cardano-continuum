package chain

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/blinklabs-io/gouroboros/ledger/common"
	ocommon "github.com/blinklabs-io/gouroboros/protocol/common"
	"github.com/blinklabs-io/gouroboros/protocol/localstatequery"
	mock "github.com/blinklabs-io/ouroboros-mock"

	"github.com/chrisguiney/cardano-continuum/internal/genesis"
	"github.com/chrisguiney/cardano-continuum/internal/keys"
	"github.com/chrisguiney/cardano-continuum/internal/rng"
)

func TestGenesisGuard(t *testing.T) {
	want := Genesis{
		Magic: 42, Start: time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC),
		EpochLength: 200, SecurityParam: 10, SlotLength: time.Second,
	}
	tests := []struct {
		name string
		mod  func(*Genesis)
		err  error
	}{
		{"match", func(*Genesis) {}, nil},
		{"other start", func(g *Genesis) { g.Start = g.Start.Add(time.Second) }, ErrGenesisMismatch},
		{"other k", func(g *Genesis) { g.SecurityParam = 5 }, ErrGenesisMismatch},
		{"other epoch length", func(g *Genesis) { g.EpochLength = 100 }, ErrGenesisMismatch},
		{"other slot length", func(g *Genesis) { g.SlotLength = 2 * time.Second }, ErrGenesisMismatch},
		{"other magic", func(g *Genesis) { g.Magic = 43 }, ErrGenesisMismatch},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := want
			tc.mod(&got)
			if err := Guard(want, got); !errors.Is(err, tc.err) {
				t.Errorf("Guard = %v, want %v", err, tc.err)
			}
		})
	}
	for _, magic := range []uint32{764824073, 1, 2, 4} {
		g := want
		g.Magic = magic
		if err := Guard(g, g); !errors.Is(err, ErrPublicNetwork) {
			t.Errorf("magic %d: Guard = %v, want ErrPublicNetwork", magic, err)
		}
	}
}

// TestParseShelleyGenesis reads back what internal/genesis renders, so the
// engine can guard a node against the genesis files it was deployed with.
func TestParseShelleyGenesis(t *testing.T) {
	s := genesis.Default()
	s.SystemStart = time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	var seed rng.Seed
	ks, err := keys.New(seed, 0)
	if err != nil {
		t.Fatal(err)
	}
	b, err := genesis.Render(s, ks)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseShelleyGenesis(b.Shelley)
	if err != nil {
		t.Fatal(err)
	}
	want := Genesis{Magic: s.Magic, Start: s.SystemStart, EpochLength: uint64(s.EpochLength),
		SecurityParam: uint64(s.SecurityParam), SlotLength: s.SlotLength}
	if err := Guard(want, got); err != nil {
		t.Fatal(err)
	}
	if w := uint64(3 * float64(s.SecurityParam) / s.ActiveSlotsCoeff); got.StabilityWindow() != w {
		t.Errorf("stability window %d, want 3k/f = %d", got.StabilityWindow(), w)
	}
	if (Genesis{SecurityParam: 5}).StabilityWindow() != 0 {
		t.Error("a genesis without f has a stability window")
	}
	if _, err := ParseShelleyGenesis([]byte(`{"networkMagic": 42}`)); err == nil {
		t.Error("a genesis without systemStart parsed")
	}
}

// serveMock accepts one connection on a loopback port and joins it to a
// mock node playing conv. It returns the address to dial.
func serveMock(t *testing.T, conv []mock.ConversationEntry) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	t.Cleanup(func() { _ = ln.Close(); <-done })
	go func() {
		defer close(done)
		c, err := ln.Accept()
		if err != nil {
			return
		}
		m := mock.NewConnection(mock.ProtocolRoleClient, conv)
		go func() { _, _ = io.Copy(m, c); _ = m.Close() }()
		_, _ = io.Copy(c, m)
		_ = c.Close()
	}()
	return ln.Addr().String()
}

// A point the node no longer holds (it keeps the last k ledger states) is
// an acquire failure, reported rather than answered from the tip.
func TestUtxosAtPointRefusesAPointTooOld(t *testing.T) {
	at := Point{Slot: 7, Hash: common.Blake2b256{1}}
	addr := serveMock(t, []mock.ConversationEntry{
		mock.ConversationEntryHandshakeRequestGeneric,
		mock.ConversationEntryHandshakeNtCResponse,
		mock.LocalStateQueryAcquire(ocommon.NewPoint(at.Slot, at.Hash[:])),
		mock.LocalStateQueryFailure(localstatequery.AcquireFailurePointTooOld),
	})
	q := NewQuerier(Node{Network: "tcp", Address: addr, Magic: mock.MockNetworkMagic})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := q.UtxosAtPoint(ctx, at, nil)
	if !errors.Is(err, localstatequery.ErrAcquireFailurePointTooOld) {
		t.Fatalf("UtxosAtPoint = %v, want point too old", err)
	}
}
