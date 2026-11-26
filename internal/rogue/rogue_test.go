package rogue

import (
	"context"
	"io"
	"log/slog"
	"net"
	"sync"
	"testing"
	"time"

	ouroboros "github.com/blinklabs-io/gouroboros"
	"github.com/blinklabs-io/gouroboros/protocol/chainsync"
	pcommon "github.com/blinklabs-io/gouroboros/protocol/common"
	"github.com/blinklabs-io/gouroboros/protocol/peersharing"
	"github.com/blinklabs-io/gouroboros/protocol/txsubmission"
	"go.uber.org/goleak"

	"github.com/chrisguiney/cardano-continuum/internal/adversary"
	"github.com/chrisguiney/cardano-continuum/internal/chain"
)

const testMagic = 42

func devnet(magic uint32) chain.Genesis {
	return chain.Genesis{Magic: magic, Start: time.Unix(0, 0).UTC(), EpochLength: 600, SecurityParam: 5, SlotLength: time.Second}
}

func cleared(t *testing.T) adversary.Clearance {
	t.Helper()
	c, err := adversary.Clear(devnet(testMagic), devnet(testMagic))
	if err != nil {
		t.Fatalf("clear devnet: %v", err)
	}
	return c
}

func discard() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// honest is an in-process gouroboros node-to-node server that mounts the
// protocols a rogue attacks. Each accepted connection runs the real server
// state machines, so the server's reaction to a rogue is a node's reaction.
type honest struct {
	ln    net.Listener
	magic uint32

	mu     sync.Mutex // protects conns and closed
	conns  []*ouroboros.Connection
	closed bool
}

func newHonest(t *testing.T, magic uint32) *honest {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	h := &honest{ln: ln, magic: magic}
	go h.accept()
	t.Cleanup(h.close)
	return h
}

func (h *honest) addr() string { return h.ln.Addr().String() }

func (h *honest) accept() {
	for {
		conn, err := h.ln.Accept()
		if err != nil {
			return
		}
		h.serve(conn)
	}
}

func (h *honest) serve(conn net.Conn) {
	oc, err := ouroboros.NewConnection(
		ouroboros.WithConnection(conn),
		ouroboros.WithNetworkMagic(h.magic),
		ouroboros.WithNodeToNode(true),
		ouroboros.WithServer(true),
		ouroboros.WithPeerSharing(true),
		ouroboros.WithChainSyncConfig(chainsync.NewConfig(
			chainsync.WithFindIntersectFunc(func(chainsync.CallbackContext, []pcommon.Point) (pcommon.Point, chainsync.Tip, error) {
				return pcommon.Point{}, chainsync.Tip{}, chainsync.ErrIntersectNotFound
			}),
			chainsync.WithRequestNextFunc(func(chainsync.CallbackContext) error { return nil }),
		)),
		ouroboros.WithPeerSharingConfig(peersharing.NewConfig(
			// Answer with a bounded, fixed set, never more than asked for.
			peersharing.WithShareRequestFunc(func(_ peersharing.CallbackContext, amount int) ([]peersharing.PeerAddress, error) {
				peers := []peersharing.PeerAddress{
					{IP: net.ParseIP("127.0.0.2"), Port: 3001},
					{IP: net.ParseIP("127.0.0.3"), Port: 3001},
				}
				if amount < len(peers) {
					peers = peers[:amount]
				}
				return peers, nil
			}),
		)),
		ouroboros.WithTxSubmissionConfig(txsubmission.NewConfig(
			txsubmission.WithRequestTxIdsFunc(func(txsubmission.CallbackContext, bool, uint16, uint16) ([]txsubmission.TxIdAndSize, error) {
				return nil, nil
			}),
		)),
	)
	if err != nil {
		_ = conn.Close()
		return
	}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		_ = oc.Close()
		return
	}
	h.conns = append(h.conns, oc)
	h.mu.Unlock()
	go func() {
		err := <-oc.ErrorChan()
		_ = err // the error is the expected disconnect; closing is the response
		_ = oc.Close()
	}()
}

func (h *honest) close() {
	h.mu.Lock()
	h.closed = true
	conns := h.conns
	h.mu.Unlock()
	_ = h.ln.Close()
	for _, oc := range conns {
		_ = oc.Close()
	}
}

func newPeer(t *testing.T, addr string) *Peer {
	t.Helper()
	p, err := New(Config{Addr: addr, Magic: testMagic, Clearance: cleared(t), Settle: 2 * time.Second}, discard())
	if err != nil {
		t.Fatalf("new peer: %v", err)
	}
	return p
}

// TestNewRefusesWithoutClearance pins the Clearance guard: a rogue cannot be
// built against a node that has not been cleared as the run's devnet.
func TestNewRefusesWithoutClearance(t *testing.T) {
	if _, err := New(Config{Addr: "127.0.0.1:1", Magic: testMagic}, discard()); err == nil {
		t.Fatal("New accepted a peer without a clearance")
	}
	if _, err := New(Config{Addr: "127.0.0.1:1", Magic: testMagic, Clearance: cleared(t)}, discard()); err != nil {
		t.Fatalf("New refused a cleared peer: %v", err)
	}
}

// handshakeDisconnects are the behaviours that must make an honest node drop
// the peer outright.
var handshakeDisconnects = []Behaviour{
	EmptyVersions, UnknownVersions, WrongMagic, OversizePropose, NotCbor, WrongProtocol,
	ChainSyncNoAgency, KeepAliveNoAgency, PeerShareNoAgency,
}

func TestBehavioursDisconnect(t *testing.T) {
	t.Cleanup(func() { goleak.VerifyNone(t, goleak.IgnoreCurrent()) })
	h := newHonest(t, testMagic)
	for _, b := range handshakeDisconnects {
		t.Run(string(b), func(t *testing.T) {
			p := newPeer(t, h.addr())
			out, err := p.Run(context.Background(), b)
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			if out.Reaction != Disconnected {
				t.Fatalf("%s: reaction %q (%s), want disconnected", b, out.Reaction, out.Detail)
			}
			if out.Finding {
				t.Fatalf("%s: unexpected finding: %s", b, out.Detail)
			}
			noninterference(t, h)
		})
	}
}

// TestBehavioursNeverFatal covers the behaviours whose exact reaction depends
// on the node's timeouts. None may be fatal (an accepted invalid message, a
// stall or a crash), and each must leave the node able to serve a fresh peer.
func TestBehavioursNeverFatal(t *testing.T) {
	t.Cleanup(func() { goleak.VerifyNone(t, goleak.IgnoreCurrent()) })
	h := newHonest(t, testMagic)
	for _, b := range []Behaviour{PeerShareAbuse, ExceedAckWindow, DuplicateTxIds, NeverDeliver, StopMidConvo, Slowloris} {
		t.Run(string(b), func(t *testing.T) {
			p := newPeer(t, h.addr())
			out, err := p.Run(context.Background(), b)
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			if out.Reaction.Fatal() {
				t.Fatalf("%s: fatal reaction %q: %s", b, out.Reaction, out.Detail)
			}
			t.Logf("%s: reaction=%s elapsed=%s detail=%s", b, out.Reaction, out.Elapsed.Round(time.Millisecond), out.Detail)
			noninterference(t, h)
		})
	}
}

// noninterference proves a rogue did not stall or crash the node: a fresh
// honest peer still completes a handshake.
func noninterference(t *testing.T, h *honest) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", h.addr(), 3*time.Second)
	if err != nil {
		t.Fatalf("honest dial after rogue: %v", err)
	}
	defer func() { _ = conn.Close() }()
	raw, err := proposeVersions(defaultVersion, testMagic)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeSegment(conn, handshakeProto, raw); err != nil {
		t.Fatalf("honest propose after rogue: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	proto, payload, err := readSegment(conn)
	if err != nil {
		t.Fatalf("node did not answer an honest handshake after a rogue run (stalled?): %v", err)
	}
	if proto != handshakeProto {
		t.Fatalf("honest reply on protocol %d", proto)
	}
	mt, err := msgType(payload)
	if err != nil {
		t.Fatalf("decode honest reply: %v", err)
	}
	if mt != 1 { // MessageTypeAcceptVersion
		t.Fatalf("node did not accept an honest handshake after a rogue run: message type %d", mt)
	}
}

// TestCraftDeterministic pins that the bytes a behaviour sends are a function
// of its inputs alone, so a run replays identically.
func TestCraftDeterministic(t *testing.T) {
	a, err := proposeVersions(defaultVersion, testMagic)
	if err != nil {
		t.Fatal(err)
	}
	b, err := proposeVersions(defaultVersion, testMagic)
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) {
		t.Fatal("proposeVersions is not deterministic")
	}
	p1, p2 := padding(64), padding(64)
	if string(p1) != string(p2) {
		t.Fatal("padding is not deterministic")
	}
	if len(padding(64)) == len(padding(65)) {
		t.Fatal("padding ignores its length")
	}
}

// TestExpectationsComplete pins that every behaviour declares an expected
// reaction, so no behaviour runs without an oracle.
func TestExpectationsComplete(t *testing.T) {
	for _, b := range Behaviours() {
		if b.Expect() == "" {
			t.Errorf("%s has no expected reaction", b)
		}
		if _, ok := actions[b]; !ok {
			t.Errorf("%s has no action", b)
		}
	}
}
