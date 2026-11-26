package forger

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"sync"

	ouroboros "github.com/blinklabs-io/gouroboros"
	"github.com/blinklabs-io/gouroboros/protocol/blockfetch"
	"github.com/blinklabs-io/gouroboros/protocol/chainsync"
	pcommon "github.com/blinklabs-io/gouroboros/protocol/common"
)

// server answers the peers that connect to the forger's listener.
type server struct {
	log      *slog.Logger
	magic    uint32
	listener net.Listener
	st       *shared
}

// accept serves every peer that connects until ctx is cancelled, and
// returns once every connection it accepted is closed.
func (v *server) accept(ctx context.Context) error {
	var wg sync.WaitGroup
	defer wg.Wait()
	stop := context.AfterFunc(ctx, func() { _ = v.listener.Close() })
	defer stop()
	for {
		conn, err := v.listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("accept peer: %w", err)
		}
		wg.Go(func() { v.serve(ctx, conn) })
	}
}

// serve runs one peer connection to its end. A peer that drops the
// connection is the expected answer to an invalid block, so its error is
// logged and is not the node's.
func (v *server) serve(ctx context.Context, conn net.Conn) {
	key := conn.RemoteAddr().String()
	host, _, err := net.SplitHostPort(key)
	if err != nil {
		host = key
	}
	log := v.log.With("peer", host, "remote", key)
	v.st.join(key, PeerID(host))
	defer v.st.leave(key)
	// The handshake blocks; closing the socket is what interrupts it.
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	bf, err := blockfetch.NewConfig(blockfetch.WithRequestRangeFunc(v.requestRange))
	if err != nil {
		log.Error("peer setup failed", "error", err)
		_ = conn.Close()
		return
	}
	oc, err := ouroboros.NewConnection(
		ouroboros.WithConnection(conn),
		ouroboros.WithNetworkMagic(v.magic),
		ouroboros.WithNodeToNode(true),
		ouroboros.WithServer(true),
		ouroboros.WithChainSyncConfig(chainsync.NewConfig(
			chainsync.WithFindIntersectFunc(v.findIntersect),
			chainsync.WithRequestNextFunc(v.requestNext),
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

func (v *server) findIntersect(ctx chainsync.CallbackContext, points []pcommon.Point) (pcommon.Point, chainsync.Tip, error) {
	return v.st.intersect(ctx.ConnectionId.RemoteAddr, ctx.Server, points)
}

func (v *server) decide(ctx chainsync.CallbackContext, park bool) (reply, PeerID, error) {
	return v.st.decide(ctx.ConnectionId.RemoteAddr, ctx.Server, park)
}

// requestNext answers one request. A peer at its tip is told to await, and
// is marked waiting only if there is still nothing for it once that message
// is out: the answer loop sends to waiting peers, and must not get ahead of
// the await.
func (v *server) requestNext(ctx chainsync.CallbackContext) error {
	r, peer, err := v.decide(ctx, false)
	if err != nil {
		return err
	}
	if err := r.send(); err != nil {
		return fmt.Errorf("answer chain-sync request: %w", err)
	}
	if r.step.Move != Wait {
		v.logServed(peer, r.step)
		return nil
	}
	if r, _, err = v.decide(ctx, true); err != nil || r.step.Move == Wait {
		return err
	}
	if err := r.send(); err != nil {
		return fmt.Errorf("answer chain-sync request after await: %w", err)
	}
	v.logServed(peer, r.step)
	return nil
}

func (v *server) logServed(peer PeerID, step Step) {
	if step.Move != Forward {
		return
	}
	kind, rogue := v.st.rogueKind(step.Block.Hash)
	if rogue {
		v.log.Info("served invalid header", "peer", string(peer), "kind", string(kind),
			"hash", fmt.Sprintf("%x", step.Block.Hash), "block_slot", step.Block.Slot)
	}
}

// answer sends waiting peers what a chain change gave them.
func (v *server) answer(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-v.st.changed:
		}
		out, peers := v.st.waiters()
		for i, r := range out {
			if err := r.send(); err != nil {
				// The connection is gone; its serve goroutine reports it.
				v.log.Warn("send to waiting peer failed", "peer", string(peers[i]), "error", err)
				continue
			}
			v.logServed(peers[i], r.step)
		}
	}
}

// requestRange serves the blocks between two points of any fork the forger
// knows, bodies as forged.
func (v *server) requestRange(ctx blockfetch.CallbackContext, start, end pcommon.Point) error {
	var from, to [32]byte
	copy(from[:], start.Hash)
	copy(to[:], end.Hash)
	blocks, ok := v.st.blocks(from, to)
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
	kind, rogue := v.st.rogueKind(to)
	if rogue {
		v.log.Info("served invalid block body", "kind", string(kind), "hash", fmt.Sprintf("%x", to),
			"remote", ctx.ConnectionId.RemoteAddr.String())
	}
	return ctx.Server.BatchDone()
}
