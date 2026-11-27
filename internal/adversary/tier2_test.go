package adversary

import (
	"errors"
	"math"
	"testing"
)

func approx(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// TestPoolRewardSaturationCaps pins that a pool past the saturation size earns
// no more than one exactly at it: the reward formula caps stake at z0.
func TestPoolRewardSaturationCaps(t *testing.T) {
	t.Parallel()
	p := RewardParams{Influence: 0.3, OptimalPools: 10, Pot: 1e9}
	z0 := p.z0()
	at := p.PoolReward(z0, 0)
	over := p.PoolReward(z0*2, 0)
	if !approx(at, over) {
		t.Fatalf("reward at z0 %.3f != over z0 %.3f: saturation not capped", at, over)
	}
	under := p.PoolReward(z0/2, 0)
	if under >= at {
		t.Fatalf("under-saturated reward %.3f not below capped %.3f", under, at)
	}
}

// TestPledgeInfluenceRewardsPledge pins that, at equal stake, more pledge earns
// more: the a0 term rewards pledge.
func TestPledgeInfluenceRewardsPledge(t *testing.T) {
	t.Parallel()
	p := RewardParams{Influence: 0.3, OptimalPools: 10, Pot: 1e9}
	low := p.PoolReward(0.05, 0.0)
	high := p.PoolReward(0.05, 0.05)
	if high <= low {
		t.Fatalf("pledged reward %.3f not above unpledged %.3f", high, low)
	}
}

// TestSybilSplitEscapesSaturation pins the saturation side of the trade-off:
// an over-saturated owner earns more by splitting into pools that each fit
// under z0.
func TestSybilSplitEscapesSaturation(t *testing.T) {
	t.Parallel()
	p := RewardParams{Influence: 0.3, OptimalPools: 10, Pot: 1e9}
	// z0 = 0.1; a single 0.3-stake pool is heavily over-saturated.
	rows := p.SybilImpact(0.3, 0.0, 3)
	owner := rows[0]
	if owner.Delta() <= 0 {
		t.Fatalf("splitting an over-saturated owner did not raise reward: %+v", owner)
	}
}

// TestSybilSplitDilutesPledge pins the pledge side: with stake under z0 and
// full pledge, splitting lowers reward because each pool's pledge bonus shrinks.
func TestSybilSplitDilutesPledge(t *testing.T) {
	t.Parallel()
	p := RewardParams{Influence: 0.3, OptimalPools: 10, Pot: 1e9}
	// Stake 0.06 < z0 = 0.1, so saturation does not bite; pledge is the whole
	// stake, so only the pledge term moves.
	one := p.Reward(Sybil{Stake: 0.06, Pledge: 0.06, Pools: 1})
	many := p.Reward(Sybil{Stake: 0.06, Pledge: 0.06, Pools: 3})
	if many >= one {
		t.Fatalf("splitting a fully-pledged under-saturated owner did not lower reward: one %.4f, many %.4f", one, many)
	}
}

// TestProposalSpamCeilingBinds pins that the cost ceiling, through the shared
// Ledger, binds the spam at the budget: exactly Count() proposals charge, the
// next is refused.
func TestProposalSpamCeilingBinds(t *testing.T) {
	t.Parallel()
	s := ProposalSpam{Deposit: 100_000_000_000, Budget: 1_000_000_000_000, ExpiryEpochs: 6, HonestRatifyEpochs: 2}
	if s.Count() != 10 {
		t.Fatalf("count = %d, want 10", s.Count())
	}
	l := NewLedger()
	charged, err := s.ChargeAll(l, 1, 0)
	if !errors.Is(err, ErrCeiling) {
		t.Fatalf("ChargeAll ended with %v, want a cost-ceiling refusal", err)
	}
	if charged != s.Count() {
		t.Fatalf("charged %d proposals, want %d", charged, s.Count())
	}
}

// TestProposalSpamThroughputUnchanged pins the model's claim that spam does not
// change honest ratification latency: only the deposit locked moves.
func TestProposalSpamThroughputUnchanged(t *testing.T) {
	t.Parallel()
	s := ProposalSpam{Deposit: 100_000_000_000, Budget: 500_000_000_000, ExpiryEpochs: 6, HonestRatifyEpochs: 2}
	rows := s.Impact()
	for _, r := range rows {
		switch r.Metric {
		case "honest_ratify_epochs":
			if r.Delta() != 0 {
				t.Fatalf("honest ratification latency moved under spam: %+v", r)
			}
		case "deposit_locked_ada":
			if r.Delta() <= 0 {
				t.Fatalf("spam locked no deposit: %+v", r)
			}
		}
	}
}

// TestMempoolFloodRaisesLatency pins that an outbidding flood delays honest
// confirmation, and that an equal run with no flood does not.
func TestMempoolFloodRaisesLatency(t *testing.T) {
	t.Parallel()
	f := MempoolFlood{
		BlockBytes: 90_000, SlotSeconds: 1, HonestTxBytes: 1_000, HonestTxs: 3,
		FloodBytes: 900_000, FloodOutbids: true,
	}
	control, treated := f.Measure()
	cp := percentile(control.Latency, 0.99)
	tp := percentile(treated.Latency, 0.99)
	if tp <= cp {
		t.Fatalf("flood did not raise honest latency: control %.0fs, treated %.0fs", cp, tp)
	}
	// Flood of 900k bytes over a 90k block budget is ~10 blocks ahead.
	if tp < 10 {
		t.Fatalf("treated p99 latency %.0fs below the ~10 blocks the flood occupies", tp)
	}
}

// TestMempoolFloodNoOutbidNoDelay pins that a flood that does not outbid does
// not delay honest confirmation.
func TestMempoolFloodNoOutbidNoDelay(t *testing.T) {
	t.Parallel()
	f := MempoolFlood{
		BlockBytes: 90_000, SlotSeconds: 1, HonestTxBytes: 1_000, HonestTxs: 3,
		FloodBytes: 900_000, FloodOutbids: false,
	}
	control, treated := f.Measure()
	if percentile(treated.Latency, 0.99) != percentile(control.Latency, 0.99) {
		t.Fatal("a non-outbidding flood changed honest latency")
	}
}
