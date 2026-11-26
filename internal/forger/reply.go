package forger

import (
	"github.com/blinklabs-io/gouroboros/protocol/chainsync"
	pcommon "github.com/blinklabs-io/gouroboros/protocol/common"
)

// session is one inbound peer connection. Its fields are guarded by
// shared.mu.
type session struct {
	peer   PeerID
	cursor Cursor
	// waiting is set once the peer has been told to await: until the next
	// reply nothing else is sent on its chain-sync, so the answer loop may.
	waiting bool
	server  *chainsync.Server
}

// reply is one chain-sync message for a peer, decided under the lock and
// sent outside it.
type reply struct {
	server *chainsync.Server
	step   Step
	tip    chainsync.Tip
	to     pcommon.Point
}

func (r reply) send() error {
	switch r.step.Move {
	case Forward:
		// The server takes the whole block and sends its header.
		return r.server.RollForward(BlockType, r.step.Block.CBOR, r.tip)
	case Backward:
		return r.server.RollBackward(r.to, r.tip)
	}
	return r.server.AwaitReply()
}

// nextReply decides s's next message and moves its cursor. The caller holds
// the lock that guards ch and s.
func nextReply(ch *Chain, s *session) reply {
	step := ch.Next(s.peer, s.cursor)
	r := reply{server: s.server, step: step}
	switch step.Move {
	case Forward:
		s.cursor = Cursor{Hash: step.Block.Hash}
	case Backward:
		s.cursor = step.To
		r.to = pointOf(ch, step.To)
	}
	r.tip = viewTip(ch, s)
	return r
}

func pointOf(ch *Chain, c Cursor) pcommon.Point {
	if c.Origin {
		return pcommon.NewPointOrigin()
	}
	b, _ := ch.Block(c.Hash)
	return pcommon.NewPoint(b.Slot, c.Hash[:])
}

// viewTip is the tip advertised to s: the end of its own view, so that an
// offered block is what the peer believes the forger selected.
func viewTip(ch *Chain, s *session) chainsync.Tip {
	t := ch.Tip()
	if suffix := ch.v.suffix(s.peer, s.cursor); len(suffix) > 0 {
		t = suffix[len(suffix)-1].Tip()
	}
	if t.Origin {
		return chainsync.Tip{Point: pcommon.NewPointOrigin()}
	}
	return chainsync.Tip{Point: pcommon.NewPoint(t.Slot, t.Hash[:]), BlockNumber: t.Number}
}
