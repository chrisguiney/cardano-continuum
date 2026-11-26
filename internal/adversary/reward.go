package adversary

import (
	"fmt"
	"math"
	"strings"
)

// Reward modelling for the sybil / pledge-splitting adversary. The fake chain
// pays no rewards (internal/fakechain never accrues a reward balance), so this
// is a standalone model of the Shelley reward formula, not the fake ledger's
// own output. It reimplements the non-myopic pool reward from the Shelley
// ledger specification (Cardano.Ledger.Shelley.Rewards.poolReward), with
// apparent performance fixed at one, so block-production variance is not
// modelled; a report using it must say so.
//
// The sybil attack is one owner running many small pools instead of one large
// one (pledge splitting). The formula pulls two ways on it: splitting escapes
// the saturation cap (z0), which raises reward, but it dilutes the pledge in
// each pool, which lowers the a0 pledge bonus. SybilImpact reports both so the
// net is visible for a given parameter set.

// RewardParams are the protocol parameters the reward formula reads. Stakes
// are relative to the total active stake, in the unit interval.
type RewardParams struct {
	// Influence is a0, the pledge influence factor (mainnet 0.3).
	Influence float64
	// OptimalPools is k (nOpt); the saturation size z0 is 1/k.
	OptimalPools float64
	// Pot is R, the lovelace available to all pools this epoch.
	Pot float64
}

func (p RewardParams) z0() float64 { return 1 / p.OptimalPools }

// PoolReward is the reward a pool of relative stake sigma and relative pledge
// pledge earns under p, with apparent performance one. Shelley specification,
// section "Rewards", equation for f(s, sigma).
func (p RewardParams) PoolReward(sigma, pledge float64) float64 {
	z0 := p.z0()
	sp := math.Min(sigma, z0)
	s := math.Min(pledge, z0)
	// R/(1+a0) * ( sp + s*a0 * (sp - s*(z0 - sp)/z0) / z0 )
	bonus := s * p.Influence * (sp - s*(z0-sp)/z0) / z0
	return p.Pot / (1 + p.Influence) * (sp + bonus)
}

// Sybil is one owner's stake and pledge, relative to the total active stake,
// and the number of pools it is split across.
type Sybil struct {
	Stake  float64
	Pledge float64
	Pools  int
}

// Reward is the owner's total reward under p when its stake and pledge are
// split evenly across its pools.
func (p RewardParams) Reward(s Sybil) float64 {
	n := max(s.Pools, 1)
	per := p.PoolReward(s.Stake/float64(n), s.Pledge/float64(n))
	return per * float64(n)
}

// SybilImpact compares one pool holding all of the owner's stake and pledge
// (the control) against splitting it across split pools (the attack). Metric
// rows: the owner's reward, the per-pool saturation headroom, and the reward a
// matching fully-pledged single pool would earn, so the pledge penalty is
// visible.
func (p RewardParams) SybilImpact(stake, pledge float64, split int) []Impact {
	one := Sybil{Stake: stake, Pledge: pledge, Pools: 1}
	many := Sybil{Stake: stake, Pledge: pledge, Pools: split}
	z0 := p.z0()
	return []Impact{
		{Metric: "owner_reward", Control: p.Reward(one), Treated: p.Reward(many)},
		{Metric: "pools", Control: 1, Treated: float64(split)},
		{Metric: "relative_stake_per_pool", Control: stake, Treated: stake / float64(max(split, 1))},
		{Metric: "saturated_pools", Control: oversat(stake, z0), Treated: oversat(stake/float64(max(split, 1)), z0) * float64(split)},
		{Metric: "reward_if_fully_pledged", Control: p.Reward(Sybil{Stake: stake, Pledge: stake, Pools: 1}), Treated: p.Reward(Sybil{Stake: stake, Pledge: stake, Pools: split})},
	}
}

// oversat is 1 when a pool of relative stake sigma is over the saturation size
// z0, else 0.
func oversat(sigma, z0 float64) float64 {
	if sigma > z0 {
		return 1
	}
	return 0
}

// SybilTable renders a sybil impact as a fixed-width table of reward in ADA.
func SybilTable(rows []Impact) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%-26s %14s %14s %14s\n", "metric", "one pool", "split", "delta")
	for _, r := range rows {
		fmt.Fprintf(&b, "%-26s %14s %14s %14s\n", r.Metric, num(r.Control), num(r.Treated), num(r.Delta()))
	}
	return b.String()
}
