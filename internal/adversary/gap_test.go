package adversary

import (
	"strings"
	"testing"

	"github.com/blinklabs-io/gouroboros/ledger/common"

	"github.com/chrisguiney/cardano-continuum/internal/genesis"
)

var (
	mainnet = Budgets{Tx: common.ExUnits{Memory: 16_500_000, Steps: 10_000_000_000}, Block: common.ExUnits{Memory: 72_000_000, Steps: 20_000_000_000}}
	gapped  = Budgets{Tx: common.ExUnits{Memory: 140_000_000, Steps: 20_000_000_000}, Block: mainnet.Block}
)

func TestGapTarget(t *testing.T) {
	if _, why := mainnet.GapTarget(); !strings.Contains(why, "fits") {
		t.Fatalf("consistent budgets gave why %q", why)
	}
	mem, why := gapped.GapTarget()
	if why != "" {
		t.Fatal(why)
	}
	if mem <= gapped.Block.Memory || mem > gapped.Tx.Memory || mem*stepsPerMemory > gapped.Block.Steps {
		t.Fatalf("target %d outside the gap (%d, %d] or past the steps budget", mem, gapped.Block.Memory, gapped.Tx.Memory)
	}
	// 140M memory with 10B steps: a script can spend at most 10B/160 =
	// 62.5M memory, below the block's 72M, so no real script lands in
	// the gap.
	starved := gapped
	starved.Tx.Steps = 10_000_000_000
	if _, why := starved.GapTarget(); !strings.Contains(why, "steps") {
		t.Fatalf("a steps-starved gap gave why %q", why)
	}
}

func TestGapPlanAndScore(t *testing.T) {
	v := fakeView{bal: 10_000_000_000, remaining: 1_000_000_000, budgets: mainnet}
	if s := scores(t, []string{string(ExUnitsGap)}, v); s[ExUnitsGap.Kind()].Value != 0 {
		t.Fatalf("scored with no gap: %+v", s[ExUnitsGap.Kind()])
	}
	if _, why := Plan(ExUnitsGap, v, nil); why == "" {
		t.Fatal("planned a gap step on consistent budgets")
	}
	v.budgets = gapped
	if s := scores(t, []string{string(ExUnitsGap)}, v); s[ExUnitsGap.Kind()].Value <= 0 {
		t.Fatalf("not scored with a gap: %+v", s[ExUnitsGap.Kind()])
	}
	st, why := Plan(ExUnitsGap, v, nil)
	if why != "" || st.Attack != ExUnitsGap || st.Memory <= gapped.Block.Memory {
		t.Fatalf("step %+v, why %q", st, why)
	}
	w := GapExpect()
	if !w.Accept || len(w.Classes) != 1 {
		t.Fatalf("gap verdict %v: want accepted or ExUnitsTooBigUTxO", w)
	}
}

// TestGapGenesisOverrides: the attack's overrides make a genesis Check
// refuses without the allow flag and accepts with it, with a real gap.
func TestGapGenesisOverrides(t *testing.T) {
	s := genesis.Default()
	for _, o := range GenesisOverrides(ExUnitsGap) {
		ov, err := genesis.ParseOverride(o)
		if err != nil {
			t.Fatal(err)
		}
		ov.Apply(&s)
	}
	if err := genesis.Check(s); err == nil {
		t.Fatal("the gap genesis passed Check without the allow flag")
	}
	s.Allow.InconsistentExUnits = true
	if err := genesis.Check(s); err != nil {
		t.Fatal(err)
	}
	b := Budgets{Tx: common.ExUnits{Memory: int64(s.TxExUnits.Memory), Steps: int64(s.TxExUnits.Steps)},
		Block: common.ExUnits{Memory: int64(s.BlockExUnits.Memory), Steps: int64(s.BlockExUnits.Steps)}}
	if _, why := b.GapTarget(); why != "" {
		t.Fatalf("the overrides leave no reachable gap: %s", why)
	}
	if len(GenesisOverrides(DustFlood)) != 0 {
		t.Fatal("dust-flood needs a genesis override")
	}
}
