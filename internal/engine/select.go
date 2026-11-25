package engine

import (
	"cmp"
	"slices"
)

// Coin is a spendable output offered to a Selector.
type Coin struct {
	Ref      Ref
	Lovelace uint64
}

// Selector picks the inputs of a payment from an actor's spendable, unreserved
// confirmed outputs. It must be a pure function of its arguments. The planner
// replaces the M1 LargestFirst.
type Selector interface {
	// Select returns coins from avail worth at least need, or nil if avail
	// cannot cover it. avail is sorted by Ref.
	Select(avail []Coin, need uint64) []Coin
}

// LargestFirst takes the largest coins until the need is met, Ref breaking
// ties, which keeps the input count low.
type LargestFirst struct{}

// Select implements Selector.
func (LargestFirst) Select(avail []Coin, need uint64) []Coin {
	sorted := slices.Clone(avail)
	slices.SortStableFunc(sorted, func(a, b Coin) int { return cmp.Compare(b.Lovelace, a.Lovelace) })
	var sum uint64
	for i, c := range sorted {
		sum += c.Lovelace
		if sum >= need {
			return sorted[:i+1]
		}
	}
	return nil
}
