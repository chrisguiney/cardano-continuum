package engine

import (
	"github.com/blinklabs-io/gouroboros/ledger/common"

	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/feeds"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/population"
)

// The invariant monitors' sources live in internal/engine/internal/feeds.
type (
	// Viewer asks a node for UTxOs at a settled point; see feeds.Viewer.
	Viewer = feeds.Viewer
	// ViewAnswer is a node's answer to a view query; see feeds.Answer.
	ViewAnswer = feeds.Answer
	// TipSource reports each followed pool's newest blocks; see
	// feeds.TipSource.
	TipSource = feeds.TipSource
)

// settled is the engine's side of the view monitor.
type settled struct{ e *Engine }

func (s settled) Confirmed() eventlog.ChainPoint { return s.e.win.Confirmed() }

// Addresses are every actor's wallet address and the faucet's, in actor
// order.
func (s settled) Addresses() []common.Address {
	e := s.e
	out := make([]common.Address, 0, len(e.co.IDs())+1)
	for _, id := range e.co.IDs() {
		out = append(out, e.co.Rec(id).Wallet.Address())
	}
	if e.cfg.Faucet > 0 {
		out = append(out, e.co.Wallet(population.ActorID(faucetActor)).Address())
	}
	return out
}

func (s settled) Owner(a common.Address) (population.ActorID, bool) {
	return owners(s).Owner(a)
}
