package engine

import (
	"testing"
	"time"

	"github.com/chrisguiney/cardano-continuum/internal/adversary"
	"github.com/chrisguiney/cardano-continuum/internal/build"
	"github.com/chrisguiney/cardano-continuum/internal/fakechain"
	"github.com/chrisguiney/cardano-continuum/internal/genesis"
	"github.com/chrisguiney/cardano-continuum/internal/oracle"
)

// gapConfig is attackConfig with the exunits-block-gap adversary on the
// inconsistent genesis its scenario needs.
func gapConfig(t testing.TB, seed uint64) Config {
	t.Helper()
	cfg := attackConfig(t, seed, 0, adversary.ExUnitsGap)
	s := genesis.Default()
	for _, o := range adversary.GenesisOverrides(adversary.ExUnitsGap) {
		ov, err := genesis.ParseOverride(o)
		if err != nil {
			t.Fatal(err)
		}
		ov.Apply(&s)
	}
	s.Allow.InconsistentExUnits = true
	pp, err := fakechain.ParamsFor(s)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ChainParams = &pp
	cfg.Build = build.ParamsFromConway(&pp, 0)
	cfg.Build.SystemStart, cfg.Build.SlotLength = time.Unix(0, 0).UTC(), time.Second
	return cfg
}

// gapTxs are the transactions the gap adversary sent.
func gapTxs(t *testing.T, w *world) map[TxID]bool {
	t.Helper()
	out := map[TxID]bool{}
	for _, a := range attacks(t, w) {
		if a.Name == string(adversary.ExUnitsGap) {
			for _, id := range a.Txs {
				out[id] = true
			}
		}
	}
	return out
}

// TestGapNeverForged: the gap adversary's transactions are admitted and
// never forged, and the run files no fatal finding.
func TestGapNeverForged(t *testing.T) {
	t.Parallel()
	w, m := attackRun(t, gapConfig(t, 7))
	sent := gapTxs(t, w)
	if len(sent) == 0 {
		t.Fatalf("the gap adversary sent nothing: %+v", m)
	}
	for _, b := range w.chain.Blocks() {
		for _, id := range b.Txs {
			if sent[TxID(id)] {
				t.Fatalf("gap transaction %x forged in block %d", id, b.Number)
			}
		}
	}
	for _, f := range findings(t, w.log) {
		if f.Severity == oracle.Fatal {
			t.Errorf("fatal finding: %s: %s", f.Kind, f.Summary)
		}
	}
}

// TestGapOverfilledBlockIsFatal: a fake forger that packs the mempool past
// the block budget, gap transactions included, files a fatal
// block_exunits_over_limit finding.
func TestGapOverfilledBlockIsFatal(t *testing.T) {
	t.Parallel()
	cfg := gapConfig(t, 7)
	var faults []fakechain.Scheduled
	for slot := uint64(30); slot < uint64(attackUntil/1000); slot += 10 {
		faults = append(faults, fakechain.Scheduled{Slot: slot, Fault: fakechain.Overfill{}})
	}
	w := newWorld(t, cfg, faults...)
	w.mem = &memStore{log: w.log}
	w.run(attackUntil, 0, nil)
	for _, f := range findings(t, w.log) {
		if f.Kind == oracle.BlockOverBudget && f.Severity == oracle.Fatal {
			return
		}
	}
	t.Fatal("an over-budget block filed no fatal finding")
}

// TestGapIdleOnConsistentGenesis: on the default genesis the gap
// adversary never attacks.
func TestGapIdleOnConsistentGenesis(t *testing.T) {
	t.Parallel()
	w, _ := attackRun(t, attackConfig(t, 7, 0, adversary.ExUnitsGap))
	if n := len(gapTxs(t, w)); n != 0 {
		t.Fatalf("the gap adversary attacked a consistent genesis %d times", n)
	}
}
