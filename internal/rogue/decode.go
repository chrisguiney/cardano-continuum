package rogue

import (
	"fmt"

	"github.com/blinklabs-io/gouroboros/cbor"
	"github.com/blinklabs-io/gouroboros/protocol"
	"github.com/blinklabs-io/gouroboros/protocol/txsubmission"
)

// msgType reads the message type from a mini-protocol message: the first item
// of the CBOR array [messageType, ...]. It is how a rogue tells an accept from
// a refuse without decoding the whole reply.
func msgType(payload []byte) (uint, error) {
	var arr []cbor.RawMessage
	if _, err := cbor.Decode(payload, &arr); err != nil {
		return 0, fmt.Errorf("rogue: decode message array: %w", err)
	}
	if len(arr) == 0 {
		return 0, fmt.Errorf("rogue: empty message array")
	}
	var mt uint
	if _, err := cbor.Decode(arr[0], &mt); err != nil {
		return 0, fmt.Errorf("rogue: decode message type: %w", err)
	}
	return mt, nil
}

// peerCount reports how many addresses a peer-sharing reply carries, so the
// caller can check the node bounded its answer to the ask.
func peerCount(payload []byte) (int, error) {
	var arr []cbor.RawMessage
	if _, err := cbor.Decode(payload, &arr); err != nil {
		return 0, fmt.Errorf("rogue: decode peer-sharing array: %w", err)
	}
	if len(arr) < 2 {
		return 0, fmt.Errorf("rogue: peer-sharing reply has %d items", len(arr))
	}
	var addrs []cbor.RawMessage
	if _, err := cbor.Decode(arr[1], &addrs); err != nil {
		return 0, fmt.Errorf("rogue: decode peer list: %w", err)
	}
	return len(addrs), nil
}

// makeTxIds returns n distinct 32-byte transaction ids, or, when dup is set, n
// ids of which the first two are identical: the duplicate a node must reject.
func makeTxIds(n int, dup bool) [][32]byte {
	out := make([][32]byte, 0, n)
	for i := range n {
		var id [32]byte
		id[0] = byte(i + 1)
		out = append(out, id)
	}
	if dup && n >= 2 {
		out[1] = out[0]
	}
	return out
}

// txIdsReply encodes a tx-submission MsgReplyTxIds announcing ids, each with a
// nominal size. A node that has requested no ids, or that sees the same id
// twice, must reject it.
func txIdsReply(ids [][32]byte) []byte {
	pairs := make([]txsubmission.TxIdAndSize, 0, len(ids))
	for _, id := range ids {
		pairs = append(pairs, txsubmission.TxIdAndSize{
			TxId: txsubmission.TxId{EraId: 6, TxId: id},
			Size: 256,
		})
	}
	msg := &txsubmission.MsgReplyTxIds{
		MessageBase: protocol.MessageBase{MessageType: txsubmission.MessageTypeReplyTxIds},
		TxIds:       pairs,
	}
	raw, err := cbor.Encode(msg)
	if err != nil {
		// Encoding fixed-shape gouroboros types cannot fail; a failure is a
		// programming error, and an empty reply still exercises the node.
		return nil
	}
	return raw
}
