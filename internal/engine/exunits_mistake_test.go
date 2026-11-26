package engine

import (
	"testing"

	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/mistake"
)

// overDeclaredIntents returns the intents whose attempts carried the
// over-declaration, and those that later sent an attempt without it: the
// retry with a corrected budget.
func overDeclaredIntents(t *testing.T, l eventlog.Reader) (made, fixed map[uint64]bool) {
	t.Helper()
	made, fixed = map[uint64]bool{}, map[uint64]bool{}
	for _, r := range logRecords(t, l) {
		if r.Kind != KindSubmitted {
			continue
		}
		var s Submitted
		if err := eventlog.Decode(r.Body, &s); err != nil {
			t.Fatal(err)
		}
		switch {
		case s.Mistakes.Has(mistake.ExUnitsOverDeclared):
			made[s.Intent] = true
		case made[s.Intent]:
			fixed[s.Intent] = true
		}
		if s.Mistakes.Has(mistake.ExUnitsUnderDeclared) {
			t.Fatalf("intent %d carries the withheld exunits_under_declared", s.Intent)
		}
	}
	return made, fixed
}

// TestOverDeclaredRetried: careless actors over-declare execution units,
// the node answers ExUnitsTooBigUTxO, they retry with the corrected budget,
// and the oracle files nothing. Ten seeds keep the clean run clean.
func TestOverDeclaredRetried(t *testing.T) {
	t.Parallel()
	var made, fixed int
	for seed := uint64(1); seed <= 10; seed++ {
		cfg := clumsyConfig(t, seed)
		w := newWorld(t, cfg)
		p := cleanRun(t, w, cfg.Slots.EpochStart(1))
		for _, f := range findings(t, p.log) {
			t.Errorf("seed %d: %s %s: %s", seed, f.Severity, f.Kind, f.Summary)
		}
		m, x := overDeclaredIntents(t, p.log)
		made, fixed = made+len(m), fixed+len(x)
	}
	if made == 0 || fixed == 0 {
		t.Fatalf("over-declarations %d, retried with a corrected budget %d", made, fixed)
	}
}

// TestOverDeclaredInGapNeverForged: on the exunits-block-gap genesis an
// honest over-declaration is admitted and never forged; it expires, the
// actor's retries and patience take over, and nothing is filed.
func TestOverDeclaredInGapNeverForged(t *testing.T) {
	t.Parallel()
	base := clumsyConfig(t, 3)
	gap := gapConfig(t, 3)
	base.ChainParams, base.Build = gap.ChainParams, gap.Build
	w := newWorld(t, base)
	p := cleanRun(t, w, base.Slots.EpochStart(2))
	for _, f := range findings(t, p.log) {
		t.Errorf("%s %s: %s", f.Severity, f.Kind, f.Summary)
	}
	made, _ := overDeclaredIntents(t, p.log)
	if len(made) == 0 {
		t.Fatal("no over-declaration on the gap genesis")
	}
	sent := map[TxID]bool{}
	for _, r := range logRecords(t, p.log) {
		var s Submitted
		if r.Kind == KindSubmitted && eventlog.Decode(r.Body, &s) == nil && s.Mistakes.Has(mistake.ExUnitsOverDeclared) {
			sent[s.ID] = true
		}
	}
	for _, b := range w.chain.Blocks() {
		for _, id := range b.Txs {
			if sent[TxID(id)] {
				t.Fatalf("over-declared %x forged in block %d", id, b.Number)
			}
		}
	}
}
