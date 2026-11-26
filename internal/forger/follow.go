package forger

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	ouroboros "github.com/blinklabs-io/gouroboros"
	"github.com/blinklabs-io/gouroboros/ledger/conway"
	"github.com/blinklabs-io/gouroboros/protocol/chainsync"
	pcommon "github.com/blinklabs-io/gouroboros/protocol/common"
)

// retryEvery is the pause before the next upstream is tried.
const retryEvery = 2 * time.Second

// follow keeps the followed chain in step with an upstream node, moving to
// the next upstream whenever one fails, until ctx is cancelled.
func (n *Node) follow(ctx context.Context) error {
	for i := 0; ; i++ {
		addr := n.cfg.Upstreams[i%len(n.cfg.Upstreams)]
		err := n.followOne(ctx, addr)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		n.log.Warn("upstream lost", "upstream", addr, "error", err)
		n.mu.Lock()
		n.synced = false
		n.mu.Unlock()
		retry := time.NewTimer(retryEvery)
		select {
		case <-ctx.Done():
			retry.Stop()
			return ctx.Err()
		case <-retry.C:
		}
	}
}

// followOne follows addr until the connection ends.
func (n *Node) followOne(ctx context.Context, addr string) error {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("dial upstream %s: %w", addr, err)
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	// The roll-forward callback fetches the body over the same connection.
	var oc *ouroboros.Connection
	fetch := func(p pcommon.Point) ([]byte, error) {
		blk, err := oc.BlockFetch().Client.GetBlock(p)
		if err != nil {
			return nil, err
		}
		return blk.Cbor(), nil
	}
	oc, err = ouroboros.NewConnection(
		ouroboros.WithConnection(conn),
		ouroboros.WithNetworkMagic(n.cfg.Params.Magic),
		ouroboros.WithNodeToNode(true),
		ouroboros.WithKeepAlive(true),
		ouroboros.WithChainSyncConfig(chainsync.NewConfig(
			chainsync.WithRollForwardFunc(func(_ chainsync.CallbackContext, _ uint, h any, tip chainsync.Tip) error {
				return n.rollForward(h, tip, fetch)
			}),
			chainsync.WithRollBackwardFunc(n.rollBackward),
			chainsync.WithAwaitReplyFunc(func(chainsync.CallbackContext) error {
				n.setSynced()
				return nil
			}),
		)),
	)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("handshake with upstream %s: %w", addr, err)
	}
	defer func() { _ = oc.Close() }()
	n.log.Info("following upstream", "upstream", addr)
	if err := oc.ChainSync().Client.Sync(n.intersectPoints()); err != nil {
		return fmt.Errorf("start chain-sync with %s: %w", addr, err)
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-oc.ErrorChan():
		if err == nil {
			err = errors.New("connection closed")
		}
		return err
	}
}

// intersectPoints offers the upstream the followed tip, a few blocks behind
// it at doubling distances, and the origin.
func (n *Node) intersectPoints() []pcommon.Point {
	n.mu.Lock()
	defer n.mu.Unlock()
	var out []pcommon.Point
	for back := 0; back < len(n.chain.followed); back = 2*back + 1 {
		e := n.chain.followed[len(n.chain.followed)-1-back]
		out = append(out, pcommon.NewPoint(e.Slot, e.Hash[:]))
	}
	return append(out, pcommon.NewPointOrigin())
}

func (n *Node) setSynced() {
	n.mu.Lock()
	n.synced = true
	n.mu.Unlock()
}

func (n *Node) rollForward(header any, tip chainsync.Tip, fetch func(pcommon.Point) ([]byte, error)) error {
	h, ok := header.(*conway.ConwayBlockHeader)
	if !ok {
		return fmt.Errorf("upstream sent a %T: the forger follows Conway chains only", header)
	}
	b := blockOf(h)
	var err error
	// The upstream may have switched away from this block already and no
	// longer serve it. Failing the connection is the recovery: the next
	// one intersects at the followed tip and gets the rollback.
	if b.CBOR, err = fetch(pcommon.NewPoint(b.Slot, b.Hash[:])); err != nil {
		return fmt.Errorf("fetch block %x at slot %d: %w", b.Hash[:8], b.Slot, err)
	}
	n.mu.Lock()
	err = n.chain.Extend(b)
	kind, rogue := n.rogue[b.Hash]
	if string(tip.Point.Hash) == string(b.Hash[:]) {
		n.synced = true
	}
	n.mu.Unlock()
	if err != nil {
		return err
	}
	if rogue {
		// The upstream is an honest node, and this is a block it had to
		// refuse.
		n.log.Error("upstream adopted an invalid block", "kind", string(kind),
			"hash", fmt.Sprintf("%x", b.Hash), "block_slot", b.Slot)
	}
	n.wake()
	return nil
}

func (n *Node) rollBackward(_ chainsync.CallbackContext, p pcommon.Point, _ chainsync.Tip) error {
	var hash [32]byte
	copy(hash[:], p.Hash)
	n.mu.Lock()
	ok := n.chain.Rollback(len(p.Hash) == 0, hash)
	n.mu.Unlock()
	if !ok {
		return fmt.Errorf("upstream rolled back to %x at slot %d, which is not on the followed chain", p.Hash, p.Slot)
	}
	n.wake()
	return nil
}

// blockOf reads what the forger needs from a followed header.
func blockOf(h *conway.ConwayBlockHeader) Block {
	return Block{
		Slot: h.Body.Slot, Number: h.Body.BlockNumber, Hash: h.Hash(), Prev: h.Body.PrevHash,
		Header: h.Cbor(), VRFOutput: h.Body.VrfResult.Output, Issuer: h.Body.IssuerVkey,
		Counter: uint64(h.Body.OpCert.SequenceNumber),
	}
}
