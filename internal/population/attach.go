package population

import "math/rand/v2"

// PoolStake is a pool's attractiveness input for delegation.
type PoolStake struct {
	ID    ActorID
	Stake uint64
}

// Attach picks a pool for a new delegator by preferential attachment: the
// chance of a pool is proportional to max(Stake, floor), so well-staked pools
// attract more and a pool with no stake is still chosen at the floor rate.
// The floor is a modelling choice and the caller supplies it. The result
// depends on the order of stake, so callers pass it sorted by ID. ok is false
// when there is nothing to pick.
func Attach(r *rand.Rand, stake []PoolStake, floor uint64) (id ActorID, ok bool) {
	weight := func(s PoolStake) float64 { return float64(max(s.Stake, floor)) }
	total := 0.0
	for _, s := range stake {
		total += weight(s)
	}
	if total <= 0 {
		return 0, false
	}
	x := r.Float64() * total
	for _, s := range stake {
		if w := weight(s); x < w {
			return s.ID, true
		} else {
			x -= w
		}
	}
	return stake[len(stake)-1].ID, true
}
