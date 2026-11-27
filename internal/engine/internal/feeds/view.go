package feeds

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"

	"github.com/blinklabs-io/gouroboros/ledger/common"

	"github.com/chrisguiney/cardano-continuum/internal/chain"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/wire"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/oracle"
	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

// Viewer asks a node for the UTxOs paying addrs in the ledger state at
// point at (local-state-query acquired at that point). A live run's is a
// chain.Querier; tests script one.
type Viewer interface {
	View(ctx context.Context, at chain.Point, addrs []common.Address) (Answer, error)
}

// Answer is a node's answer to a view query: who answered, at which
// point, and the UTxOs.
type Answer struct {
	Node  oracle.Node
	At    chain.Point
	Utxos []common.Utxo
}

// Settled is the engine's side of the view check.
type Settled interface {
	// Confirmed is the engine's settled point.
	Confirmed() eventlog.ChainPoint
	// Addresses are the addresses whose balances the engine settles.
	Addresses() []common.Address
	// Owner is the actor an address belongs to.
	Owner(a common.Address) (population.ActorID, bool)
}

// View asks the Viewer for the actors' balances at the engine's settled
// point and logs the answer as a MonitorSnapshot input, which the engine
// compares with its own.
type View struct {
	viewer  Viewer
	engine  Settled
	clock   Clock
	every   sched.Time
	log     *slog.Logger
	viewed  eventlog.ChainPoint // settled point of the last query
	at      sched.Time          // time of the last query
	queried bool
}

// NewView returns the view feed, asking v at each new settled point at
// most once every every of simulated time (zero: at every new point); a
// nil v asks nothing.
func NewView(v Viewer, e Settled, c Clock, every sched.Time, log *slog.Logger) *View {
	return &View{viewer: v, engine: e, clock: c, every: every, log: log}
}

// Poll queries the node when due. A node that cannot answer leaves the
// check not evaluable this time; it does not stop the run.
func (v *View) Poll(ctx context.Context, l Loop) error {
	if v.viewer == nil {
		return nil
	}
	conf, now := v.engine.Confirmed(), v.clock.Now()
	if v.queried && (samePoint(conf, v.viewed) || now < v.at+v.every) {
		return nil
	}
	at, ok := chain.PointFromLog(conf)
	if !ok {
		return fmt.Errorf("%w: confirmed slot %d", wire.ErrBadPoint, conf.Slot)
	}
	v.viewed, v.at, v.queried = conf, now, true
	ans, err := v.viewer.View(ctx, at, v.engine.Addresses())
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		v.log.Warn("view query failed", "slot", at.Slot, "err", err)
		return nil
	}
	body, err := eventlog.Encode(wire.MonitorSnapshot{Views: []oracle.ViewCheck{{
		Node: ans.Node, NodeAt: ans.At, Chain: oracle.Balances(ans.Utxos, v.engine.Owner),
	}}})
	if err != nil {
		return err
	}
	return l.Feed(ctx, eventlog.Record{At: l.Now(), Kind: eventlog.KindMonitorSnapshot, Body: body})
}

func samePoint(a, b eventlog.ChainPoint) bool {
	return a.Slot == b.Slot && bytes.Equal(a.Hash, b.Hash)
}
