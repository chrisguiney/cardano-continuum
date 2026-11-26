// Package rogue holds the tier-2 rogue node-to-node peer: an adversary that
// speaks the Ouroboros mini-protocols on a raw muxer (internal/rogue/wire.go)
// so it can break rules an honest peer, built on gouroboros's own muxer and
// state machines, never could. Each behaviour provokes one expected reaction
// from an honest node: a disconnect, or a limit the node applies, without the
// node crashing, leaking a goroutine or connection, stalling its other peers,
// or accepting the invalid message. A reaction other than the expected one
// is a finding; an accepted invalid message is fatal.
//
// The behaviours, and the honest reaction each must provoke:
//
//	behaviour                    expected reaction
//	handshake-empty-versions     disconnect (handshake refusal)
//	handshake-unknown-versions   disconnect (version mismatch)
//	handshake-wrong-magic        disconnect (version mismatch)
//	handshake-oversize           disconnect (message over the handshake limit)
//	handshake-not-cbor           disconnect (decode error)
//	pre-handshake-wrong-protocol disconnect (a message before the handshake)
//	chainsync-without-agency     disconnect (a server message from the client)
//	keepalive-without-agency     disconnect (a server message from the client)
//	peersharing-without-agency   disconnect (a server message from the client)
//	peersharing-amount-abuse     limit (a bounded reply, never over the ask)
//	txsubmission-exceed-ack      disconnect (more ids than were requested)
//	txsubmission-duplicate-ids   disconnect (a tx id announced twice)
//	txsubmission-never-deliver   disconnect (a requested tx withheld past timeout)
//	stop-mid-conversation        disconnect (the node's own protocol timeout)
//	slowloris                    disconnect (a segment dribbled within timeouts)
//
// A rogue peer never runs outside a devnet: New refuses a peer without an
// adversary.Clearance, the same genesis-hash gate the transaction-level
// adversaries use.
package rogue

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"slices"
	"time"

	"github.com/chrisguiney/cardano-continuum/internal/adversary"
)

// defaultVersion is the node-to-node version a rogue proposes when a caller
// names none: Conway-capable and widely supported.
const defaultVersion = 13

// Errors.
var (
	// ErrNotCleared: a peer built without a devnet clearance.
	ErrNotCleared = errors.New("rogue: refusing to attack without a devnet clearance")
	// ErrUnknown: a behaviour the package does not have.
	ErrUnknown = errors.New("rogue: unknown behaviour")
)

// Behaviour is one rogue misbehaviour on the raw muxer.
type Behaviour string

// The rogue behaviours.
const (
	EmptyVersions     Behaviour = "handshake-empty-versions"
	UnknownVersions   Behaviour = "handshake-unknown-versions"
	WrongMagic        Behaviour = "handshake-wrong-magic"
	OversizePropose   Behaviour = "handshake-oversize"
	NotCbor           Behaviour = "handshake-not-cbor"
	WrongProtocol     Behaviour = "pre-handshake-wrong-protocol"
	ChainSyncNoAgency Behaviour = "chainsync-without-agency"
	KeepAliveNoAgency Behaviour = "keepalive-without-agency"
	PeerShareNoAgency Behaviour = "peersharing-without-agency"
	PeerShareAbuse    Behaviour = "peersharing-amount-abuse"
	ExceedAckWindow   Behaviour = "txsubmission-exceed-ack"
	DuplicateTxIds    Behaviour = "txsubmission-duplicate-ids"
	NeverDeliver      Behaviour = "txsubmission-never-deliver"
	StopMidConvo      Behaviour = "stop-mid-conversation"
	Slowloris         Behaviour = "slowloris"
)

var behaviours = []Behaviour{
	EmptyVersions, UnknownVersions, WrongMagic, OversizePropose, NotCbor,
	WrongProtocol, ChainSyncNoAgency, KeepAliveNoAgency, PeerShareNoAgency,
	PeerShareAbuse, ExceedAckWindow, DuplicateTxIds, NeverDeliver,
	StopMidConvo, Slowloris,
}

// Behaviours returns every rogue behaviour, in a fixed order.
func Behaviours() []Behaviour { return slices.Clone(behaviours) }

// ParseBehaviour returns the behaviour named s.
func ParseBehaviour(s string) (Behaviour, error) {
	if b := Behaviour(s); slices.Contains(behaviours, b) {
		return b, nil
	}
	return "", fmt.Errorf("%w %q", ErrUnknown, s)
}

// Reaction is what an honest node did to a rogue peer. It is the oracle's
// alphabet: Disconnected and Limited are healthy; the rest are findings.
type Reaction string

// The reactions.
const (
	// Disconnected: the node closed the connection.
	Disconnected Reaction = "disconnected"
	// Limited: the node kept the connection but bounded its reply to the
	// protocol's limit.
	Limited Reaction = "limited"
	// Ignored: the node neither closed nor answered within the window.
	Ignored Reaction = "ignored"
	// Accepted: the node took the invalid message as valid. Fatal.
	Accepted Reaction = "accepted"
	// Stalled: the node stopped answering a healthy request after the rogue
	// acted. Fatal.
	Stalled Reaction = "stalled"
	// Crashed: the node is gone (its liveness probe failed). Fatal.
	Crashed Reaction = "crashed"
)

// Fatal reports whether reaction r is a fatal finding.
func (r Reaction) Fatal() bool { return r == Accepted || r == Stalled || r == Crashed }

// expect is the healthy reaction each behaviour must provoke.
var expect = map[Behaviour]Reaction{
	EmptyVersions:     Disconnected,
	UnknownVersions:   Disconnected,
	WrongMagic:        Disconnected,
	OversizePropose:   Disconnected,
	NotCbor:           Disconnected,
	WrongProtocol:     Disconnected,
	ChainSyncNoAgency: Disconnected,
	KeepAliveNoAgency: Disconnected,
	PeerShareNoAgency: Disconnected,
	PeerShareAbuse:    Limited,
	ExceedAckWindow:   Disconnected,
	DuplicateTxIds:    Disconnected,
	NeverDeliver:      Disconnected,
	StopMidConvo:      Disconnected,
	Slowloris:         Disconnected,
}

// Expect returns the healthy reaction behaviour b must provoke from an honest
// node. A run whose observed reaction differs is a finding.
func (b Behaviour) Expect() Reaction { return expect[b] }

// DialFunc opens a connection, matching net.Dialer.DialContext, so a test can
// inject a pipe and a live run a real dialer.
type DialFunc func(ctx context.Context, network, addr string) (net.Conn, error)

// Config is what a Peer runs with.
type Config struct {
	// Addr is the honest node's node-to-node host:port.
	Addr string
	// Magic is the devnet's network magic, carried in the handshake.
	Magic uint32
	// Version is the node-to-node version a handshake proposes; zero picks a
	// Conway-capable default.
	Version uint16
	// Clearance proves the target is the run's devnet. Without a cleared one
	// New refuses.
	Clearance adversary.Clearance
	// Settle bounds how long a Peer waits for the node's reaction before
	// classifying it. Zero picks a default.
	Settle time.Duration
	// Dial opens the connection; nil uses a net.Dialer.
	Dial DialFunc
}

// defaultSettle bounds the wait for a reaction. A node's own handshake and
// mini-protocol timeouts are up to ten seconds (handshake.ProposeTimeout), so
// a stall-based behaviour needs at least that long before it is declared hung.
const defaultSettle = 12 * time.Second

// Peer is a rogue node-to-node peer. It runs one behaviour per Run and holds
// no connection between runs.
type Peer struct {
	cfg Config
	log *slog.Logger
}

// New checks cfg and builds a Peer. It refuses a peer without a devnet
// clearance, so a rogue can never be pointed at a public network.
func New(cfg Config, log *slog.Logger) (*Peer, error) {
	if !cfg.Clearance.Cleared() {
		return nil, ErrNotCleared
	}
	if cfg.Addr == "" {
		return nil, errors.New("rogue: needs a node address")
	}
	if log == nil {
		return nil, errors.New("rogue: needs a logger")
	}
	cfg.Version = clampVersion(cfg.Version)
	if cfg.Settle <= 0 {
		cfg.Settle = defaultSettle
	}
	return &Peer{cfg: cfg, log: log.With("component", "rogue")}, nil
}

// Outcome is what one behaviour did: the reaction the node gave and whether it
// matched the behaviour's expectation.
type Outcome struct {
	Behaviour Behaviour
	// Reaction is what the node did.
	Reaction Reaction
	// Finding is set when Reaction is not the behaviour's expectation.
	Finding bool
	// Detail names the evidence: the refusal reason, the read error, or the
	// reply seen.
	Detail string
	// Elapsed is how long the node took to react.
	Elapsed time.Duration
}

// Run performs behaviour b against the node and classifies its reaction. The
// returned error is a fault in running the attack (a dial failure), never the
// node's reaction, which is the Outcome.
func (p *Peer) Run(ctx context.Context, b Behaviour) (Outcome, error) {
	act, ok := actions[b]
	if !ok {
		return Outcome{}, fmt.Errorf("%w %q", ErrUnknown, b)
	}
	conn, err := dialConn(p.cfg.Dial, ctx, p.cfg.Addr)
	if err != nil {
		return Outcome{}, err
	}
	defer func() { _ = conn.Close() }()
	start := time.Now()
	r, detail := act(ctx, p, conn)
	out := Outcome{Behaviour: b, Reaction: r, Detail: detail, Elapsed: time.Since(start)}
	out.Finding = r != b.Expect()
	p.log.Info("rogue run",
		"behaviour", string(b), "reaction", string(r),
		"expected", string(b.Expect()), "finding", out.Finding,
		"elapsed", out.Elapsed.Round(time.Millisecond).String(), "detail", detail)
	return out, nil
}
