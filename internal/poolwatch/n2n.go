package poolwatch

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	ouroboros "github.com/blinklabs-io/gouroboros"
	"github.com/blinklabs-io/gouroboros/ledger"
	"github.com/blinklabs-io/gouroboros/protocol/chainsync"
	ocommon "github.com/blinklabs-io/gouroboros/protocol/common"

	"github.com/chrisguiney/cardano-continuum/internal/chain"
)

// N2N dials nodes over node-to-node TCP with the network magic.
type N2N struct {
	Magic uint32
	Log   *slog.Logger
}

// Dial implements Dialer.
func (n N2N) Dial(ctx context.Context, addr string) (Conn, error) {
	c := &n2nConn{events: make(chan Event, 16), done: make(chan struct{})}
	cfg := chainsync.NewConfig(
		chainsync.WithRollForwardFunc(c.forward),
		chainsync.WithRollBackwardFunc(c.backward),
	)
	var d net.Dialer
	d.Timeout = 10 * time.Second
	nc, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("poolwatch: dial %s: %w", addr, err)
	}
	opts := []ouroboros.ConnectionOptionFunc{
		ouroboros.WithConnection(nc), ouroboros.WithNetworkMagic(n.Magic),
		ouroboros.WithNodeToNode(true), ouroboros.WithKeepAlive(true),
		ouroboros.WithChainSyncConfig(cfg),
	}
	if n.Log != nil {
		opts = append(opts, ouroboros.WithLogger(n.Log))
	}
	conn, err := ouroboros.NewConnection(opts...)
	if err != nil {
		return nil, fmt.Errorf("poolwatch: handshake with %s: %w", addr, errors.Join(err, nc.Close()))
	}
	c.conn = conn
	return c, nil
}

type n2nConn struct {
	conn   *ouroboros.Connection
	events chan Event
	done   chan struct{}
	once   sync.Once
	err    error
}

// Start asks for the node's tip and syncs from it, so only blocks adopted
// after the connection arrive.
func (c *n2nConn) Start(ctx context.Context) (chain.Mark, error) {
	type res struct {
		m   chain.Mark
		err error
	}
	out := make(chan res, 1)
	go func() {
		cl := c.conn.ChainSync().Client
		tip, err := cl.GetCurrentTip()
		if err != nil {
			out <- res{err: fmt.Errorf("poolwatch: current tip: %w", err)}
			return
		}
		p, ok := chain.PointFromWire(tip.Point)
		if !ok {
			out <- res{err: fmt.Errorf("poolwatch: malformed tip at slot %d", tip.Point.Slot)}
			return
		}
		if err := cl.Sync([]ocommon.Point{tip.Point}); err != nil {
			out <- res{err: fmt.Errorf("poolwatch: sync from tip: %w", err)}
			return
		}
		out <- res{m: chain.Mark{Height: tip.BlockNumber, Point: p}}
	}()
	select {
	case r := <-out:
		return r.m, r.err
	case <-ctx.Done():
		// Closing the connection fails the pending call, so the goroutine exits.
		return chain.Mark{}, errors.Join(ctx.Err(), c.Close(), (<-out).err)
	}
}

func (c *n2nConn) Next(ctx context.Context) (Event, error) {
	select {
	case ev := <-c.events:
		return ev, nil
	case err, ok := <-c.conn.ErrorChan():
		if !ok {
			err = errors.New("connection closed")
		}
		return Event{}, fmt.Errorf("poolwatch: chain-sync: %w", err)
	case <-ctx.Done():
		return Event{}, ctx.Err()
	}
}

func (c *n2nConn) Close() error {
	c.once.Do(func() {
		close(c.done)
		c.err = c.conn.Close()
	})
	return c.err
}

func (c *n2nConn) forward(_ chainsync.CallbackContext, _ uint, data any, _ chainsync.Tip) error {
	h, ok := data.(ledger.BlockHeader)
	if !ok {
		return fmt.Errorf("poolwatch: roll forward carried %T, not a header", data)
	}
	m := chain.Mark{Height: h.BlockNumber()}
	m.Slot, m.Hash = h.SlotNumber(), h.Hash()
	return c.deliver(Event{Forward: &m})
}

func (c *n2nConn) backward(_ chainsync.CallbackContext, p ocommon.Point, _ chainsync.Tip) error {
	pt, ok := chain.PointFromWire(p)
	if !ok {
		return fmt.Errorf("poolwatch: rollback to a malformed point at slot %d", p.Slot)
	}
	return c.deliver(Event{Backward: &pt})
}

func (c *n2nConn) deliver(ev Event) error {
	select {
	case c.events <- ev:
		return nil
	case <-c.done:
		return chainsync.ErrStopSyncProcess
	}
}
