package node

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"

	ouroboros "github.com/blinklabs-io/gouroboros"
	"github.com/blinklabs-io/gouroboros/ledger"
	"github.com/blinklabs-io/gouroboros/protocol/chainsync"
	ocommon "github.com/blinklabs-io/gouroboros/protocol/common"

	"github.com/chrisguiney/cardano-continuum/internal/chain"
)

// Node dials a cardano-node's node-to-client socket. Network is "unix" for
// the node's socket path, or "tcp" when the socket is forwarded.
type Node struct {
	Network string
	Address string
	Magic   uint32
	Log     *slog.Logger
}

// Dial implements Dialer: one connection running chain-sync with full
// blocks, as node-to-client chain-sync serves them.
func (n Node) Dial(ctx context.Context) (Source, error) {
	s := &nodeSource{events: make(chan chain.Event), done: make(chan struct{})}
	cfg := chainsync.NewConfig(
		chainsync.WithRollForwardFunc(s.forward),
		chainsync.WithRollBackwardFunc(s.backward),
		chainsync.WithIntersectFoundFunc(s.found),
	)
	conn, err := n.connect(ctx, ouroboros.WithChainSyncConfig(cfg))
	if err != nil {
		return nil, err
	}
	s.conn = conn
	return s, nil
}

func (n Node) connect(ctx context.Context, opts ...ouroboros.ConnectionOptionFunc) (*ouroboros.Connection, error) {
	var d net.Dialer
	nc, err := d.DialContext(ctx, n.Network, n.Address)
	if err != nil {
		return nil, fmt.Errorf("chain: dial node %s: %w", n.Address, err)
	}
	opts = append(opts,
		ouroboros.WithConnection(nc),
		ouroboros.WithNetworkMagic(n.Magic),
		ouroboros.WithNodeToNode(false),
	)
	if n.Log != nil {
		opts = append(opts, ouroboros.WithLogger(n.Log))
	}
	conn, err := ouroboros.NewConnection(opts...)
	if err != nil {
		return nil, fmt.Errorf("chain: handshake with node %s: %w", n.Address, errors.Join(err, nc.Close()))
	}
	return conn, nil
}

// nodeSource turns gouroboros' chain-sync callbacks into Next calls. A
// callback blocks until Next takes its event, so the node is read no faster
// than the engine applies blocks.
type nodeSource struct {
	conn   *ouroboros.Connection
	events chan chain.Event
	done   chan struct{}
	once   sync.Once

	mu sync.Mutex // protects at
	at ocommon.Point
}

func (s *nodeSource) Intersect(ctx context.Context, points []chain.Point) (chain.Point, error) {
	wire := make([]ocommon.Point, len(points))
	for i, p := range points {
		wire[i] = p.Wire()
	}
	res := make(chan error, 1)
	go func() { res <- s.conn.ChainSync().Client.Sync(wire) }()
	select {
	case err := <-res:
		if errors.Is(err, chainsync.ErrIntersectNotFound) {
			return chain.Point{}, ErrNoIntersect
		}
		if err != nil {
			return chain.Point{}, fmt.Errorf("chain: find intersect: %w", err)
		}
	case <-ctx.Done():
		// Closing the connection fails Sync, so the goroutine exits.
		// Arguments evaluate left to right: close, then wait.
		return chain.Point{}, errors.Join(ctx.Err(), s.Close(), <-res)
	}
	s.mu.Lock()
	at := s.at
	s.mu.Unlock()
	p, ok := chain.PointFromWire(at)
	if !ok {
		return chain.Point{}, fmt.Errorf("chain: node answered intersect with a malformed point at slot %d", at.Slot)
	}
	return p, nil
}

func (s *nodeSource) Next(ctx context.Context) (chain.Event, error) {
	select {
	case ev := <-s.events:
		return ev, nil
	case err, ok := <-s.conn.ErrorChan():
		if !ok {
			err = errors.New("connection closed")
		}
		return chain.Event{}, fmt.Errorf("chain: chain-sync: %w", err)
	case <-ctx.Done():
		return chain.Event{}, ctx.Err()
	}
}

func (s *nodeSource) Close() error {
	var err error
	s.once.Do(func() {
		close(s.done)
		err = s.conn.Close()
	})
	return err
}

func (s *nodeSource) found(_ chainsync.CallbackContext, p ocommon.Point, _ chainsync.Tip) error {
	s.mu.Lock()
	s.at = p
	s.mu.Unlock()
	return nil
}

func (s *nodeSource) forward(_ chainsync.CallbackContext, _ uint, data any, tip chainsync.Tip) error {
	b, ok := data.(ledger.Block)
	if !ok {
		return fmt.Errorf("chain: node-to-client roll forward carried %T, not a block", data)
	}
	blk := chain.BlockFrom(b)
	return s.deliver(chain.Event{Kind: chain.Forward, Block: blk, Point: blk.Point, Tip: tipPoint(tip)})
}

func (s *nodeSource) backward(_ chainsync.CallbackContext, p ocommon.Point, tip chainsync.Tip) error {
	pt, ok := chain.PointFromWire(p)
	if !ok {
		return fmt.Errorf("chain: node rolled back to a malformed point at slot %d", p.Slot)
	}
	return s.deliver(chain.Event{Kind: chain.Backward, Point: pt, Tip: tipPoint(tip)})
}

func (s *nodeSource) deliver(ev chain.Event) error {
	select {
	case s.events <- ev:
		return nil
	case <-s.done:
		return chainsync.ErrStopSyncProcess
	}
}

func tipPoint(t chainsync.Tip) chain.Point {
	p, _ := chain.PointFromWire(t.Point)
	return p
}
