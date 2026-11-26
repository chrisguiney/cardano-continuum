package forger

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"

	ouroboros "github.com/blinklabs-io/gouroboros"
	"github.com/blinklabs-io/gouroboros/protocol/blockfetch"
	"github.com/blinklabs-io/gouroboros/protocol/chainsync"
	pcommon "github.com/blinklabs-io/gouroboros/protocol/common"
)

// session is one inbound peer connection. Its fields are guarded by
// Node.mu.
type session struct {
	peer   PeerID
	cursor Cursor
	// waiting is set once the peer has been told to await: until the next
	// reply nothing else is sent on its chain-sync, so the answer loop may.
	waiting bool
	server  *chainsync.Server
}

// accept serves every peer that connects until ctx is cancelled, and
// returns once every connection it accepted is closed.
func (n *Node) accept(ctx context.Context) error {
	var wg sync.WaitGroup
	defer wg.Wait()
	stop := context.AfterFunc(ctx, func() { _ = n.cfg.Listener.Close() })
	defer stop()
	for {
		conn, err := n.cfg.Listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("accept peer: %w", err)
		}
		wg.Go(func() { n.serve(ctx, conn) })
	}
}

// serve runs one peer connection to its end. A peer that drops the
// connection is the expected answer to an invalid block, so its error is
// logged and is not the node's.
func (n *Node) serve(ctx context.Context, conn net.Conn) {
	key := conn.RemoteAddr().String()
	host, _, err := net.SplitHostPort(key)
	if err != nil {
		host = key
	}
	log := n.log.With("peer", host, "remote", key)
	n.mu.Lock()
	n.sessions[key] = &session{peer: PeerID(host), cursor: Cursor{Origin: true}}
	n.mu.Unlock()
	defer func() {
		n.mu.Lock()
		delete(n.sessions, key)
		n.mu.Unlock()
	}()
	// The handshake blocks; closing the socket is what interrupts it.
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	bf, err := blockfetch.NewConfig(blockfetch.WithRequestRangeFunc(n.requestRange))
	if err != nil {
		log.Error("peer setup failed", "error", err)
		_ = conn.Close()
		return
	}
	oc, err := ouroboros.NewConnection(
		ouroboros.WithConnection(conn),
		ouroboros.WithNetworkMagic(n.cfg.Params.Magic),
		ouroboros.WithNodeToNode(true),
		ouroboros.WithServer(true),
		ouroboros.WithChainSyncConfig(chainsync.NewConfig(
			chainsync.WithFindIntersectFunc(n.findIntersect),
			chainsync.WithRequestNextFunc(n.requestNext),
		)),
		ouroboros.WithBlockFetchConfig(bf),
	)
	if err != nil {
		log.Warn("peer handshake failed", "error", err)
		_ = conn.Close()
		return
	}
	log.Info("peer connected")
	select {
	case <-ctx.Done():
	case err := <-oc.ErrorChan():
		log.Info("peer disconnected", "error", err)
	}
	_ = oc.Close()
}

func (n *Node) session(remote net.Addr) (*session, error) {
	if remote == nil {
		return nil, errors.New("callback without a remote address")
	}
	s, ok := n.sessions[remote.String()]
	if !ok {
		return nil, fmt.Errorf("no session for %s", remote)
	}
	return s, nil
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

// next decides s's next message and moves its cursor. The caller holds mu.
func (n *Node) next(s *session) reply {
	step := n.chain.Next(s.peer, s.cursor)
	r := reply{server: s.server, step: step}
	switch step.Move {
	case Forward:
		s.cursor = Cursor{Hash: step.Block.Hash}
	case Backward:
		s.cursor = step.To
		r.to = n.point(step.To)
	}
	r.tip = n.viewTip(s)
	return r
}

func (n *Node) point(c Cursor) pcommon.Point {
	if c.Origin {
		return pcommon.NewPointOrigin()
	}
	b, _ := n.chain.Block(c.Hash)
	return pcommon.NewPoint(b.Slot, c.Hash[:])
}

// viewTip is the tip advertised to s: the end of its own view, so that an
// offered block is what the peer believes the forger selected.
func (n *Node) viewTip(s *session) chainsync.Tip {
	t := n.chain.Tip()
	if suffix := n.chain.v.suffix(s.peer, s.cursor); len(suffix) > 0 {
		t = suffix[len(suffix)-1].Tip()
	}
	if t.Origin {
		return chainsync.Tip{Point: pcommon.NewPointOrigin()}
	}
	return chainsync.Tip{Point: pcommon.NewPoint(t.Slot, t.Hash[:]), BlockNumber: t.Number}
}

func (n *Node) findIntersect(ctx chainsync.CallbackContext, points []pcommon.Point) (pcommon.Point, chainsync.Tip, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	s, err := n.session(ctx.ConnectionId.RemoteAddr)
	if err != nil {
		return pcommon.Point{}, chainsync.Tip{}, err
	}
	s.server = ctx.Server
	cursors := make([]Cursor, 0, len(points))
	for _, p := range points {
		c := Cursor{Origin: len(p.Hash) == 0}
		copy(c.Hash[:], p.Hash)
		cursors = append(cursors, c)
	}
	at, ok := n.chain.Intersect(s.peer, cursors)
	if !ok {
		return pcommon.Point{}, n.viewTip(s), chainsync.ErrIntersectNotFound
	}
	s.cursor = at
	return n.point(at), n.viewTip(s), nil
}

// requestNext answers one request. A peer at its tip is told to await, and
// is marked waiting only if there is still nothing for it once that message
// is out: the answer loop sends to waiting peers, and must not get ahead of
// the await.
func (n *Node) requestNext(ctx chainsync.CallbackContext) error {
	r, peer, err := n.decide(ctx, false)
	if err != nil {
		return err
	}
	if err := r.send(); err != nil {
		return fmt.Errorf("answer chain-sync request: %w", err)
	}
	if r.step.Move != Wait {
		n.logServed(peer, r.step)
		return nil
	}
	if r, _, err = n.decide(ctx, true); err != nil || r.step.Move == Wait {
		return err
	}
	if err := r.send(); err != nil {
		return fmt.Errorf("answer chain-sync request after await: %w", err)
	}
	n.logServed(peer, r.step)
	return nil
}

// decide is the next message for the peer of ctx. With park, a peer with
// nothing to be sent is marked waiting.
func (n *Node) decide(ctx chainsync.CallbackContext, park bool) (reply, PeerID, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	s, err := n.session(ctx.ConnectionId.RemoteAddr)
	if err != nil {
		return reply{}, "", err
	}
	s.server = ctx.Server
	r := n.next(s)
	if park && r.step.Move == Wait {
		s.waiting = true
	}
	return r, s.peer, nil
}

func (n *Node) logServed(peer PeerID, step Step) {
	if step.Move != Forward {
		return
	}
	n.mu.Lock()
	kind, rogue := n.rogue[step.Block.Hash]
	n.mu.Unlock()
	if rogue {
		n.log.Info("served invalid header", "peer", string(peer), "kind", string(kind),
			"hash", fmt.Sprintf("%x", step.Block.Hash), "block_slot", step.Block.Slot)
	}
}

// answer sends waiting peers what a chain change gave them.
func (n *Node) answer(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-n.changed:
		}
		n.mu.Lock()
		var out []reply
		var peers []PeerID
		for _, s := range n.sessions {
			if !s.waiting {
				continue
			}
			r := n.next(s)
			if r.step.Move == Wait {
				continue
			}
			s.waiting = false
			out, peers = append(out, r), append(peers, s.peer)
		}
		n.mu.Unlock()
		for i, r := range out {
			if err := r.send(); err != nil {
				// The connection is gone; its serve goroutine reports it.
				n.log.Warn("send to waiting peer failed", "peer", string(peers[i]), "error", err)
				continue
			}
			n.logServed(peers[i], r.step)
		}
	}
}

// requestRange serves the blocks between two points of any fork the forger
// knows, bodies as forged.
func (n *Node) requestRange(ctx blockfetch.CallbackContext, start, end pcommon.Point) error {
	var from, to [32]byte
	copy(from[:], start.Hash)
	copy(to[:], end.Hash)
	n.mu.Lock()
	blocks, ok := n.chain.Range(from, to)
	n.mu.Unlock()
	if !ok {
		return ctx.Server.NoBlocks()
	}
	if err := ctx.Server.StartBatch(); err != nil {
		return fmt.Errorf("start block batch: %w", err)
	}
	for _, b := range blocks {
		if err := ctx.Server.Block(BlockType, b.CBOR); err != nil {
			return fmt.Errorf("send block %x: %w", b.Hash[:8], err)
		}
	}
	n.mu.Lock()
	kind, rogue := n.rogue[to]
	n.mu.Unlock()
	if rogue {
		n.log.Info("served invalid block body", "kind", string(kind), "hash", fmt.Sprintf("%x", to),
			"remote", ctx.ConnectionId.RemoteAddr.String())
	}
	return ctx.Server.BatchDone()
}
