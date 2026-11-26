package forger

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"time"

	ouroboros "github.com/blinklabs-io/gouroboros"
	"github.com/blinklabs-io/gouroboros/ledger/conway"
	"github.com/blinklabs-io/gouroboros/protocol/chainsync"
	pcommon "github.com/blinklabs-io/gouroboros/protocol/common"
)

// follower keeps the followed chain in step with the upstream nodes.
type follower struct {
	log       *slog.Logger
	magic     uint32
	upstreams []string
	st        *shared
}

// retryEvery is the pause before the next upstream is tried.
const retryEvery = 2 * time.Second

// follow keeps the followed chain in step with an upstream node, moving to
// the next upstream whenever one fails, until ctx is cancelled.
func (f *follower) follow(ctx context.Context) error {
	for i := 0; ; i++ {
		addr := f.upstreams[i%len(f.upstreams)]
		err := f.followOne(ctx, addr)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		f.log.Warn("upstream lost", "upstream", addr, "error", err)
		f.st.setSynced(false)
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
func (f *follower) followOne(ctx context.Context, addr string) error {
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
		ouroboros.WithNetworkMagic(f.magic),
		ouroboros.WithNodeToNode(true),
		ouroboros.WithKeepAlive(true),
		ouroboros.WithChainSyncConfig(chainsync.NewConfig(
			chainsync.WithRollForwardFunc(func(_ chainsync.CallbackContext, _ uint, h any, tip chainsync.Tip) error {
				return f.rollForward(h, tip, fetch)
			}),
			chainsync.WithRollBackwardFunc(f.rollBackward),
			chainsync.WithAwaitReplyFunc(func(chainsync.CallbackContext) error {
				f.st.setSynced(true)
				return nil
			}),
		)),
	)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("handshake with upstream %s: %w", addr, err)
	}
	defer func() { _ = oc.Close() }()
	f.log.Info("following upstream", "upstream", addr)
	if err := oc.ChainSync().Client.Sync(f.st.points()); err != nil {
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

func (f *follower) rollForward(header any, tip chainsync.Tip, fetch func(pcommon.Point) ([]byte, error)) error {
	h, ok := header.(*conway.ConwayBlockHeader)
	if !ok {
		return fmt.Errorf("upstream sent a %T: the forger follows Conway chains only", header)
	}
	b := blockOf(h)
	// The upstream may have switched away from this block already and no
	// longer serve it. Failing the connection is the recovery: the next
	// one intersects at the followed tip and gets the rollback.
	var err error
	if b.CBOR, err = fetch(pcommon.NewPoint(b.Slot, b.Hash[:])); err != nil {
		return fmt.Errorf("fetch block %x at slot %d: %w", b.Hash[:8], b.Slot, err)
	}
	kind, rogue, err := f.st.extend(b, tip)
	if err != nil {
		return err
	}
	if rogue {
		// The upstream is an honest node, and this is a block it had to
		// refuse.
		f.log.Error("upstream adopted an invalid block", "kind", string(kind),
			"hash", fmt.Sprintf("%x", b.Hash), "block_slot", b.Slot)
	}
	f.st.wake()
	return nil
}

func (f *follower) rollBackward(_ chainsync.CallbackContext, p pcommon.Point, _ chainsync.Tip) error {
	var hash [32]byte
	copy(hash[:], p.Hash)
	if !f.st.rollback(len(p.Hash) == 0, hash) {
		return fmt.Errorf("upstream rolled back to %x at slot %d, which is not on the followed chain", p.Hash, p.Slot)
	}
	f.st.wake()
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
