package engine

import (
	"context"
	"fmt"

	"github.com/blinklabs-io/gouroboros/ledger/common"

	"github.com/chrisguiney/cardano-continuum/internal/chain"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/oracle"
	"github.com/chrisguiney/cardano-continuum/internal/population"
)

// Viewer asks a node for the UTxOs paying addrs in the ledger state at
// point at (local-state-query acquired at that point). A live run's is a
// chain.Querier; tests script one.
type Viewer interface {
	View(ctx context.Context, at chain.Point, addrs []common.Address) (ViewAnswer, error)
}

// ViewAnswer is a node's answer to a view query: who answered, at which
// point, and the UTxOs.
type ViewAnswer struct {
	Node  oracle.Node
	At    chain.Point
	Utxos []common.Utxo
}

// view asks the Viewer for the actors' balances at the engine's settled
// point and logs the answer as a MonitorSnapshot input, which the engine
// compares with its own. A node that cannot answer leaves the check not
// evaluable this time; it does not stop the run.
func (r *Runner) view(ctx context.Context) error {
	if r.d.Viewer == nil {
		return nil
	}
	conf, now := r.eng.Confirmed(), r.d.Clock.Now()
	if r.queried && (samePoint(conf, r.viewed) || now < r.viewAt+r.d.ViewEvery) {
		return nil
	}
	at, ok := chain.PointFromLog(conf)
	if !ok {
		return fmt.Errorf("%w: confirmed slot %d", ErrBadPoint, conf.Slot)
	}
	r.viewed, r.viewAt, r.queried = conf, now, true
	ans, err := r.d.Viewer.View(ctx, at, r.eng.addresses())
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		r.log.Warn("view query failed", "slot", at.Slot, "err", err)
		return nil
	}
	owner := owners{r.eng}
	body, err := eventlog.Encode(MonitorSnapshot{Views: []oracle.ViewCheck{{
		Node: ans.Node, NodeAt: ans.At, Chain: oracle.Balances(ans.Utxos, owner.Owner),
	}}})
	if err != nil {
		return err
	}
	rec := eventlog.Record{At: r.eng.Now(), Kind: eventlog.KindMonitorSnapshot, Body: body}
	return r.step(ctx, func() error { return r.sess.Feed(ctx, rec) })
}

// addresses are every actor's wallet address and the faucet's, in actor
// order.
func (e *Engine) addresses() []common.Address {
	out := make([]common.Address, 0, len(e.order)+1)
	for _, id := range e.order {
		out = append(out, e.actors[id].wallet.Address())
	}
	if e.cfg.Faucet > 0 {
		out = append(out, e.walletOf(population.ActorID(faucetActor)).Address())
	}
	return out
}

// TipSource reports each followed pool's newest blocks. A live run's is the
// pool tracker; tests script one.
type TipSource interface {
	Branches(ctx context.Context) []oracle.Branch
}

// tips logs the pools' followed chains as a MonitorSnapshot input every
// ViewEvery, for the fork monitor. Nothing is logged while no pool is
// followed.
func (r *Runner) tips(ctx context.Context) error {
	if r.d.Tips == nil {
		return nil
	}
	now := r.d.Clock.Now()
	if r.tipped && now < r.tipAt+r.d.ViewEvery {
		return nil
	}
	r.tipAt, r.tipped = now, true
	bs := r.d.Tips.Branches(ctx)
	if len(bs) == 0 {
		return nil
	}
	body, err := eventlog.Encode(MonitorSnapshot{Tips: bs})
	if err != nil {
		return err
	}
	rec := eventlog.Record{At: r.eng.Now(), Kind: eventlog.KindMonitorSnapshot, Body: body}
	return r.step(ctx, func() error { return r.sess.Feed(ctx, rec) })
}
