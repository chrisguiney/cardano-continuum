package rogue

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"net"
	"time"

	"github.com/blinklabs-io/gouroboros/cbor"
	"github.com/blinklabs-io/gouroboros/muxer"
	"github.com/blinklabs-io/gouroboros/protocol"
	"github.com/blinklabs-io/gouroboros/protocol/handshake"
)

// The raw muxer wire format (Ouroboros Network Specification section 3.1,
// "Multiplexing"): every mini-protocol message is wrapped in a segment of an
// 8-byte header and a payload of at most 65535 bytes. The header is a 4-byte
// transmit timestamp, a 2-byte protocol id whose top bit is the
// responder-flag, and a 2-byte payload length, all big-endian. Working at
// this level, rather than through gouroboros's muxer, is what lets a rogue
// peer break the framing and agency rules an honest peer cannot.
const (
	headerLen         = 8
	maxPayload        = muxer.SegmentMaxPayloadLength // 65535
	responderFlag     = 0x8000
	handshakeProto    = handshake.ProtocolId // 0
	chainSyncProto    = 2
	blockFetchProto   = 3
	txSubmissionProto = 4
	keepAliveProto    = 8
	peerSharingProto  = 10
)

// writeSegment frames payload for protocolId and writes one segment to w.
// payload longer than one segment is the caller's to split; an oversize
// payload is itself a rogue behaviour, so writeSegment reports it rather
// than truncating.
func writeSegment(w io.Writer, protocolId uint16, payload []byte) error {
	if len(payload) > maxPayload {
		return fmt.Errorf("rogue: segment payload %d over %d", len(payload), maxPayload)
	}
	var hdr [headerLen]byte
	// #nosec G115 -- a wrapped unix-nano timestamp is what the wire carries.
	binary.BigEndian.PutUint32(hdr[0:4], uint32(time.Now().UnixNano()))
	binary.BigEndian.PutUint16(hdr[4:6], protocolId)
	binary.BigEndian.PutUint16(hdr[6:8], uint16(len(payload)))
	if _, err := w.Write(hdr[:]); err != nil {
		return fmt.Errorf("rogue: write segment header: %w", err)
	}
	if _, err := w.Write(payload); err != nil {
		return fmt.Errorf("rogue: write segment payload: %w", err)
	}
	return nil
}

// readSegment reads one segment from r. A read past deadline, an EOF or a
// reset is how an honest node signals it has disconnected the peer, so the
// caller treats readSegment's error as that signal, not as its own fault.
func readSegment(r io.Reader) (protocolId uint16, payload []byte, err error) {
	var hdr [headerLen]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return 0, nil, err
	}
	protocolId = binary.BigEndian.Uint16(hdr[4:6]) &^ responderFlag
	n := binary.BigEndian.Uint16(hdr[6:8])
	payload = make([]byte, n)
	if _, err := io.ReadFull(r, payload); err != nil {
		return protocolId, nil, err
	}
	return protocolId, payload, nil
}

// proposeVersions is the handshake MsgProposeVersions bytes for a single
// node-to-node version carrying magic, diffusion mode and no peer sharing.
// A rogue abuses the handshake by proposing versions the node cannot speak
// (see unknownVersions and oversizePropose); this is the honest shape it
// starts from.
func proposeVersions(version uint16, magic uint32) ([]byte, error) {
	vd, err := versionData(version, magic)
	if err != nil {
		return nil, err
	}
	return encodeProposeRaw(map[uint16]cbor.RawMessage{version: vd})
}

// versionData is the node-to-node version-data block for version: the array
// the node decodes with NewVersionDataNtN*FromCbor. Versions below 13 omit
// the peer-sharing and query fields.
func versionData(version uint16, magic uint32) (cbor.RawMessage, error) {
	var fields []any
	switch {
	case version >= 13:
		// [magic, initiatorAndResponder, peerSharing, query]
		fields = []any{magic, false, uint(0), false}
	case version >= 11:
		fields = []any{magic, false, uint(0), false}
	default:
		// NtN 7..10: [magic, initiatorAndResponder]
		fields = []any{magic, false}
	}
	raw, err := cbor.Encode(fields)
	if err != nil {
		return nil, fmt.Errorf("rogue: encode version data: %w", err)
	}
	return cbor.RawMessage(raw), nil
}

// encodeProposeRaw builds the MsgProposeVersions bytes from a raw version
// map, so a rogue can offer deliberately bad entries.
func encodeProposeRaw(versions map[uint16]cbor.RawMessage) ([]byte, error) {
	msg := &handshake.MsgProposeVersions{
		MessageBase: protocol.MessageBase{MessageType: handshake.MessageTypeProposeVersions},
		VersionMap:  versions,
	}
	raw, err := cbor.Encode(msg)
	if err != nil {
		return nil, fmt.Errorf("rogue: encode propose: %w", err)
	}
	return raw, nil
}

// message encodes a mini-protocol message as the CBOR array
// [messageType, fields...], the wire form every mini-protocol shares
// (specification section 3.3). A rogue uses it to send a message whose type
// the node does not expect in the current protocol state.
func message(messageType uint, fields ...any) ([]byte, error) {
	arr := make([]any, 0, len(fields)+1)
	arr = append(arr, messageType)
	arr = append(arr, fields...)
	raw, err := cbor.Encode(arr)
	if err != nil {
		return nil, fmt.Errorf("rogue: encode message %d: %w", messageType, err)
	}
	return raw, nil
}

// dialConn opens a TCP connection to addr using dial, or net.Dialer when dial
// is nil.
func dialConn(dial DialFunc, ctx context.Context, addr string) (net.Conn, error) {
	if dial == nil {
		d := &net.Dialer{Timeout: 5 * time.Second}
		dial = d.DialContext
	}
	conn, err := dial(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("rogue: dial %s: %w", addr, err)
	}
	return conn, nil
}

// padding returns n deterministic bytes, so an oversize message is the same
// on every replay of a seed.
func padding(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i % 251) // a prime stride, so the pattern is not byte-aligned
	}
	return b
}

// clampVersion keeps a proposed version in the range the wire encodes.
func clampVersion(v uint16) uint16 {
	if v == 0 {
		return defaultVersion
	}
	if v > math.MaxUint16-responderFlag {
		return defaultVersion
	}
	return v
}
