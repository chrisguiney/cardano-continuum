package rogue

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/blinklabs-io/gouroboros/cbor"
	"github.com/blinklabs-io/gouroboros/protocol/handshake"
	"github.com/blinklabs-io/gouroboros/protocol/keepalive"
	"github.com/blinklabs-io/gouroboros/protocol/peersharing"
	"github.com/blinklabs-io/gouroboros/protocol/txsubmission"
)

// action runs one behaviour over conn and returns the node's reaction and the
// evidence for it. An action never returns an error: a failure to read is the
// node's disconnect, which is a reaction, not a fault.
type action func(ctx context.Context, p *Peer, conn net.Conn) (Reaction, string)

var actions = map[Behaviour]action{
	EmptyVersions:     emptyVersions,
	UnknownVersions:   unknownVersions,
	WrongMagic:        wrongMagic,
	OversizePropose:   oversizePropose,
	NotCbor:           notCbor,
	WrongProtocol:     wrongProtocol,
	ChainSyncNoAgency: noAgency(chainSyncProto, chainSyncRollForward),
	KeepAliveNoAgency: noAgency(keepAliveProto, keepaliveResponse),
	PeerShareNoAgency: noAgency(peerSharingProto, peerSharingReply),
	PeerShareAbuse:    peerShareAbuse,
	ExceedAckWindow:   exceedAck,
	DuplicateTxIds:    duplicateTxIds,
	NeverDeliver:      neverDeliver,
	StopMidConvo:      stopMidConvo,
	Slowloris:         slowloris,
}

// The chain-sync, keep-alive and peer-sharing messages a client holds no
// agency to send. Sending one is the "message without agency" attack.
var (
	chainSyncRollForward = mustMsg(chainsyncMessageTypeRollForward, []byte{}, []any{uint(0), []byte{}})
	keepaliveResponse    = mustMsg(keepalive.MessageTypeKeepAliveResponse, uint(0))
	peerSharingReply     = mustMsg(peersharing.MessageTypeSharePeers, []any{})
)

const chainsyncMessageTypeRollForward = 2

// mustMsg encodes a mini-protocol message or panics; it runs once at package
// initialisation on constant input, so a failure is a programming error.
func mustMsg(messageType uint, fields ...any) []byte {
	b, err := message(messageType, fields...)
	if err != nil {
		panic(err)
	}
	return b
}

func emptyVersions(ctx context.Context, p *Peer, conn net.Conn) (Reaction, string) {
	raw, err := encodeProposeRaw(map[uint16]cbor.RawMessage{})
	if err != nil {
		return Ignored, err.Error()
	}
	return proposeAndObserve(ctx, p, conn, raw)
}

func unknownVersions(ctx context.Context, p *Peer, conn net.Conn) (Reaction, string) {
	// Version 1 is far below the oldest node-to-node version a node speaks.
	vd, err := versionData(1, p.cfg.Magic)
	if err != nil {
		return Ignored, err.Error()
	}
	raw, err := encodeProposeRaw(map[uint16]cbor.RawMessage{1: vd})
	if err != nil {
		return Ignored, err.Error()
	}
	return proposeAndObserve(ctx, p, conn, raw)
}

func wrongMagic(ctx context.Context, p *Peer, conn net.Conn) (Reaction, string) {
	raw, err := proposeVersions(p.cfg.Version, p.cfg.Magic^0x5a5a5a5a)
	if err != nil {
		return Ignored, err.Error()
	}
	return proposeAndObserve(ctx, p, conn, raw)
}

func oversizePropose(ctx context.Context, p *Peer, conn net.Conn) (Reaction, string) {
	// Pad the version map past handshake.MaxPendingMessageBytes with junk
	// version entries, each a large byte string the node must refuse to buffer.
	versions := map[uint16]cbor.RawMessage{}
	for v := uint16(1000); v < 1000+16; v++ {
		blob, err := cbor.Encode(padding(512))
		if err != nil {
			return Ignored, err.Error()
		}
		versions[v] = cbor.RawMessage(blob)
	}
	raw, err := encodeProposeRaw(versions)
	if err != nil {
		return Ignored, err.Error()
	}
	if len(raw) <= handshake.MaxPendingMessageBytes {
		return Ignored, fmt.Sprintf("propose only %d bytes, not oversize", len(raw))
	}
	return proposeAndObserve(ctx, p, conn, raw)
}

func notCbor(ctx context.Context, p *Peer, conn net.Conn) (Reaction, string) {
	// 0xFF is a CBOR break with no enclosing indefinite item: an invalid
	// top-level message the node's decoder must reject.
	junk := make([]byte, 64)
	for i := range junk {
		junk[i] = 0xff
	}
	if err := writeSegment(conn, handshakeProto, junk); err != nil {
		return Ignored, err.Error()
	}
	return awaitClose(conn, p.cfg.Settle)
}

func wrongProtocol(ctx context.Context, p *Peer, conn net.Conn) (Reaction, string) {
	// A chain-sync message before any handshake: the node holds no chain-sync
	// state yet, so it must drop the peer rather than act on it.
	if err := writeSegment(conn, chainSyncProto, chainSyncRollForward); err != nil {
		return Ignored, err.Error()
	}
	return awaitClose(conn, p.cfg.Settle)
}

// noAgency returns an action that completes a handshake, then sends on
// protocolId a message only the server may send, so the node sees the client
// act without agency.
func noAgency(protocolId uint16, msg []byte) action {
	return func(ctx context.Context, p *Peer, conn net.Conn) (Reaction, string) {
		if r, detail, ok := handshakeThen(ctx, p, conn); !ok {
			return r, detail
		}
		if err := writeSegment(conn, protocolId, msg); err != nil {
			return Ignored, err.Error()
		}
		return awaitClose(conn, p.cfg.Settle)
	}
}

func peerShareAbuse(ctx context.Context, p *Peer, conn net.Conn) (Reaction, string) {
	if r, detail, ok := handshakeThen(ctx, p, conn); !ok {
		return r, detail
	}
	// Ask for the most peers the amount byte can name. A node must bound its
	// reply to that ask, never answer with more.
	const ask = 255
	req, err := message(peersharing.MessageTypeShareRequest, uint(ask))
	if err != nil {
		return Ignored, err.Error()
	}
	if err := writeSegment(conn, peerSharingProto, req); err != nil {
		return Ignored, err.Error()
	}
	return peerShareReply(conn, p.cfg.Settle, ask)
}

func exceedAck(ctx context.Context, p *Peer, conn net.Conn) (Reaction, string) {
	if r, detail, ok := handshakeThen(ctx, p, conn); !ok {
		return r, detail
	}
	// Announce the tx-submission by sending Init, then reply with a batch of
	// ids before the node has requested any: more than the zero outstanding
	// the node has asked for, a window violation.
	if err := writeSegment(conn, txSubmissionProto, mustMsg(txsubmission.MessageTypeInit)); err != nil {
		return Ignored, err.Error()
	}
	reply := txIdsReply(makeTxIds(8, false))
	if err := writeSegment(conn, txSubmissionProto, reply); err != nil {
		return Ignored, err.Error()
	}
	return awaitClose(conn, p.cfg.Settle)
}

func duplicateTxIds(ctx context.Context, p *Peer, conn net.Conn) (Reaction, string) {
	if r, detail, ok := handshakeThen(ctx, p, conn); !ok {
		return r, detail
	}
	if err := writeSegment(conn, txSubmissionProto, mustMsg(txsubmission.MessageTypeInit)); err != nil {
		return Ignored, err.Error()
	}
	// Reply with the same tx id twice: an announcement a node must reject.
	reply := txIdsReply(makeTxIds(4, true))
	if err := writeSegment(conn, txSubmissionProto, reply); err != nil {
		return Ignored, err.Error()
	}
	return awaitClose(conn, p.cfg.Settle)
}

func neverDeliver(ctx context.Context, p *Peer, conn net.Conn) (Reaction, string) {
	if r, detail, ok := handshakeThen(ctx, p, conn); !ok {
		return r, detail
	}
	// Init and go silent: announce nothing and answer nothing. The node's
	// tx-submission timeout must drop the peer rather than wait forever.
	if err := writeSegment(conn, txSubmissionProto, mustMsg(txsubmission.MessageTypeInit)); err != nil {
		return Ignored, err.Error()
	}
	return awaitClose(conn, p.cfg.Settle)
}

func stopMidConvo(ctx context.Context, p *Peer, conn net.Conn) (Reaction, string) {
	if r, detail, ok := handshakeThen(ctx, p, conn); !ok {
		return r, detail
	}
	// Say nothing after the handshake. A node keeps a keep-alive and
	// chain-sync timeout; one of them must fire and drop the idle peer.
	return awaitClose(conn, p.cfg.Settle)
}

func slowloris(ctx context.Context, p *Peer, conn net.Conn) (Reaction, string) {
	// Dribble a handshake segment header one byte at a time, holding the
	// connection within but never completing a message. The node's read or
	// handshake timeout must fire.
	var hdr [headerLen]byte
	hdr[5] = byte(handshakeProto)
	hdr[7] = 32 // claims a 32-byte payload that never arrives in full
	for i := 0; i < len(hdr); i++ {
		if _, err := conn.Write(hdr[i : i+1]); err != nil {
			return classifyWriteErr(err)
		}
		if !sleepCtx(ctx, 300*time.Millisecond) {
			return Ignored, "context cancelled during dribble"
		}
	}
	return awaitClose(conn, p.cfg.Settle)
}

// proposeAndObserve sends a handshake propose and classifies the node's
// reaction: a close (with or without a refusal first) is a disconnect.
func proposeAndObserve(ctx context.Context, p *Peer, conn net.Conn, propose []byte) (Reaction, string) {
	if err := writeSegment(conn, handshakeProto, propose); err != nil {
		return Ignored, err.Error()
	}
	return awaitClose(conn, p.cfg.Settle)
}

// handshakeThen completes an honest handshake so a post-handshake behaviour
// can run. ok is false when the node refused or dropped the handshake itself,
// in which case the reaction and detail describe that.
func handshakeThen(ctx context.Context, p *Peer, conn net.Conn) (Reaction, string, bool) {
	raw, err := proposeVersions(p.cfg.Version, p.cfg.Magic)
	if err != nil {
		return Ignored, err.Error(), false
	}
	if err := writeSegment(conn, handshakeProto, raw); err != nil {
		return classifyReadErr(err), err.Error(), false
	}
	_ = conn.SetReadDeadline(time.Now().Add(p.cfg.Settle))
	proto, payload, err := readSegment(conn)
	if err != nil {
		return Disconnected, "node closed before accepting handshake: " + err.Error(), false
	}
	if proto != handshakeProto {
		return Ignored, fmt.Sprintf("handshake reply on protocol %d", proto), false
	}
	mt, derr := msgType(payload)
	if derr != nil {
		return Ignored, "undecodable handshake reply: " + derr.Error(), false
	}
	if mt != handshake.MessageTypeAcceptVersion {
		return Disconnected, fmt.Sprintf("handshake not accepted: message type %d", mt), false
	}
	_ = conn.SetReadDeadline(time.Time{})
	return "", "", true
}

// awaitClose reads until the node closes the connection or the window ends. A
// read error is the disconnect; a window that ends with the connection open is
// the node ignoring the peer, which the caller weighs against the expectation.
func awaitClose(conn net.Conn, settle time.Duration) (Reaction, string) {
	deadline := time.Now().Add(settle)
	_ = conn.SetReadDeadline(deadline)
	var firstRefusal string
	for {
		proto, payload, err := readSegment(conn)
		if err != nil {
			detail := "node closed the connection: " + err.Error()
			if firstRefusal != "" {
				detail = firstRefusal + "; then " + detail
			}
			if isTimeout(err) {
				return Ignored, "node left the connection open without answering"
			}
			return Disconnected, detail
		}
		if proto == handshakeProto && firstRefusal == "" {
			if mt, derr := msgType(payload); derr == nil && mt == handshake.MessageTypeRefuse {
				firstRefusal = "node refused the handshake"
			}
		}
	}
}

// peerShareReply reads the node's peer-sharing answer and checks it is bounded
// by the ask. More peers than asked for is the node failing to apply its
// limit.
func peerShareReply(conn net.Conn, settle time.Duration, ask int) (Reaction, string) {
	_ = conn.SetReadDeadline(time.Now().Add(settle))
	proto, payload, err := readSegment(conn)
	if err != nil {
		if isTimeout(err) {
			return Limited, "node answered with no peers within the window"
		}
		return Disconnected, "node closed the connection: " + err.Error()
	}
	if proto != peerSharingProto {
		return Ignored, fmt.Sprintf("reply on protocol %d, not peer-sharing", proto)
	}
	n, err := peerCount(payload)
	if err != nil {
		return Ignored, "undecodable peer-sharing reply: " + err.Error()
	}
	if n > ask {
		return Accepted, fmt.Sprintf("node shared %d peers, over the ask of %d", n, ask)
	}
	return Limited, fmt.Sprintf("node shared %d peers, within the ask of %d", n, ask)
}

func classifyWriteErr(err error) (Reaction, string) {
	return Disconnected, "node closed the connection: " + err.Error()
}

func classifyReadErr(err error) Reaction {
	if isTimeout(err) {
		return Ignored
	}
	return Disconnected
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// sleepCtx sleeps for d or until ctx is cancelled; it reports whether the full
// sleep elapsed.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
