package node

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

	"github.com/chrisguiney/cardano-continuum/internal/chain"
)

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
	t.Parallel()
	at := chain.Point{Slot: 7, Hash: common.Blake2b256{1}}
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
