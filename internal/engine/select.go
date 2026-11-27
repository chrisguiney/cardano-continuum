package engine

import (
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/wire"
	"github.com/chrisguiney/cardano-continuum/internal/plan"
)

// Coin is a spendable output offered to a Selector.
type Coin = wire.Coin

// Selector picks the inputs of a payment from an actor's spendable, unreserved
// confirmed outputs. It must be a pure function of its arguments.
type Selector interface {
	// Select returns coins from avail worth at least need, or nil if avail
	// cannot cover it. avail is sorted by Ref.
	Select(avail []Coin, need uint64) []Coin
}

// LargestFirst is the planner's coin selection (plan.Select): the largest
// coins until the need is met, Ref breaking ties, which keeps the input
// count low.
type LargestFirst struct{}

// Select implements Selector.
func (LargestFirst) Select(avail []Coin, need uint64) []Coin {
	us := make([]plan.Utxo, len(avail))
	for i, c := range avail {
		us[i] = plan.Utxo{Ref: plan.Ref{Tx: plan.TxID(c.Ref.Tx), Index: c.Ref.Index}, Lovelace: c.Lovelace}
	}
	picked := plan.Select(us, need)
	if picked == nil {
		return nil
	}
	out := make([]Coin, len(picked))
	for i, u := range picked {
		out[i] = Coin{Ref: Ref{Tx: TxID(u.Ref.Tx), Index: u.Ref.Index}, Lovelace: u.Lovelace}
	}
	return out
}
