package agenda

import (
	"math"
	"slices"

	"github.com/chrisguiney/cardano-continuum/internal/population"
)

// Owners is the optional capability of a View that knows who owns the
// wallets. A wallet's delegation reads it to spread its owner's stake: a
// view without it scores as if every wallet were its own owner.
type Owners interface {
	// Siblings are the other wallets the actor's owner still uses.
	Siblings(population.ActorID) []population.ActorID
	// Spread is the owner's delegation spread in [0, 1].
	Spread(population.ActorID) float64
}

// spreadPools is ps with each pool's ROI scaled by (1 - spread)^n, n the
// owner's other wallets delegated to it: an owner that spreads its stake
// sees the pools it already uses as worth less to its next wallet. Without
// owners, siblings or spread it returns ps itself.
func spreadPools(a population.Actor, v View, ps []PoolInfo) []PoolInfo {
	o, ok := v.(Owners)
	if !ok {
		return ps
	}

	keep := 1 - o.Spread(a.ID)
	sibs := o.Siblings(a.ID)
	if keep >= 1 || len(sibs) == 0 {
		return ps
	}

	on := map[population.ActorID]int{}
	for _, s := range sibs {
		if pool, ok := v.DelegatedTo(s); ok {
			on[pool]++
		}
	}

	out := slices.Clone(ps)
	for i := range out {
		if n := on[out[i].ID]; n > 0 {
			out[i].ROI *= math.Pow(keep, float64(n))
		}
	}
	return out
}
