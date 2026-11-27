package genesis

import (
	"bytes"
	"strings"
	"testing"

	"github.com/blinklabs-io/gouroboros/ledger/alonzo"
)

// TestRenderedLimits: the Alonzo genesis carries mainnet's limits as of
// epoch 669 (param_update rows enacted at epochs 393 and 614), not the
// 140M per-transaction memory of cardano-api's alonzoGenesisDefaults.
func TestRenderedLimits(t *testing.T) {
	t.Parallel()
	b := render(t, fixedSpec(), 1)
	ag, err := alonzo.NewAlonzoGenesisFromReader(bytes.NewReader(b.Alonzo))
	if err != nil {
		t.Fatal(err)
	}
	if tx := ag.MaxTxExUnits; tx.Mem != 16_500_000 || tx.Steps != 10_000_000_000 {
		t.Errorf("maxTxExUnits = %+v, want 16.5M / 10B", tx)
	}
	if blk := ag.MaxBlockExUnits; blk.Mem != 72_000_000 || blk.Steps != 20_000_000_000 {
		t.Errorf("maxBlockExUnits = %+v, want 72M / 20B", blk)
	}
}

func TestCheckRefusesTxAboveBlock(t *testing.T) {
	t.Parallel()
	for _, mod := range []func(*Spec){
		func(s *Spec) { s.TxExUnits.Memory = s.BlockExUnits.Memory + 1 },
		func(s *Spec) { s.TxExUnits.Steps = s.BlockExUnits.Steps + 1 },
	} {
		s := Default()
		mod(&s)
		err := Check(s)
		if err == nil || !strings.Contains(err.Error(), "maxBlockExUnits") {
			t.Fatalf("Check = %v, want a per-tx above per-block refusal", err)
		}
		s.Allow.InconsistentExUnits = true
		if err := Check(s); err != nil {
			t.Fatalf("Check with the override = %v", err)
		}
		if _, err := Render(s, store(t, 1)); err != nil {
			t.Fatalf("Render with the override = %v", err)
		}
	}
}

func TestCheckRefusesZeroLimits(t *testing.T) {
	t.Parallel()
	s := Default()
	s.BlockExUnits.Steps = 0
	s.Allow.InconsistentExUnits = true
	if err := Check(s); err == nil {
		t.Fatal("Check accepted a zero block budget")
	}
}

func TestOverride(t *testing.T) {
	t.Parallel()
	s := Default()
	for _, in := range []string{"maxTxExUnits.memory=140000000", "maxBlockExUnits.memory=62000000", "costModels.PlutusV3.length=251"} {
		o, err := ParseOverride(in)
		if err != nil {
			t.Fatalf("ParseOverride(%q): %v", in, err)
		}
		o.Apply(&s)
		if o.String() != in {
			t.Errorf("String = %q, want %q", o.String(), in)
		}
	}
	if s.TxExUnits.Memory != 140_000_000 || s.BlockExUnits.Memory != 62_000_000 || s.CostModelLen[PlutusV3] != 251 {
		t.Fatalf("overrides not applied: %+v %+v %v", s.TxExUnits, s.BlockExUnits, s.CostModelLen)
	}
	if err := Check(s); err == nil {
		t.Fatal("Check accepted overrides without the allow flags")
	}
	for _, bad := range []string{"", "maxTxExUnits.memory", "maxTxExUnits.memory=-1", "maxTxExUnits.memory=1e9", "systemStart=1", "costModels.PlutusV9.length=1"} {
		if _, err := ParseOverride(bad); err == nil {
			t.Errorf("ParseOverride(%q) accepted", bad)
		}
	}
}

// TestDefaultUnchangedBySpecHash: the limits are part of the spec, so an
// override makes a different devnet.
func TestOverrideChangesSpecHash(t *testing.T) {
	t.Parallel()
	s := Default()
	s.TxExUnits.Memory++
	if specHash(s) == specHash(Default()) {
		t.Fatal("a limit override left the spec hash unchanged")
	}
}
