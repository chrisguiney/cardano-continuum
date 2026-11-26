package genesis

import (
	"bytes"
	"slices"
	"strings"
	"testing"

	"github.com/blinklabs-io/gouroboros/ledger/alonzo"
	"github.com/blinklabs-io/gouroboros/ledger/conway"
	"github.com/blinklabs-io/plutigo/lang"
)

// TestParamCounts pins the parameter counts per language at PV10 to the
// reference name lists (plutus 1.72.0.0, PlutusLedgerApi/V{1,2,3}/ParamName.hs):
// V1 ends its original block at 166, V2 adds integerToByteString and
// byteStringToInteger at Plomin (175 + 10), V3 adds batch 5 at Plomin
// (251 + 46).
func TestParamCounts(t *testing.T) {
	want := map[Language]int{PlutusV1: 166, PlutusV2: 185, PlutusV3: 297}
	for l, n := range want {
		got, ok := ParamCount(l, 10)
		if !ok || got != n {
			t.Errorf("%v at PV10: %d %v, want %d", l, got, ok, n)
		}
	}
	if _, ok := ParamCount(PlutusV3, 12); ok {
		t.Error("PV12 has a parameter count; no cost models are shipped for it")
	}
}

// TestCostModelsMatchProtocolVersion: the default genesis carries, per
// language, exactly the parameters its protocol version defines, and no
// more than plutigo names (a longer list would be truncated silently).
func TestCostModelsMatchProtocolVersion(t *testing.T) {
	s := Default()
	ms, err := CostModels(s)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range Languages() {
		want, _ := ParamCount(l, s.ProtocolVersion[0])
		if got := len(ms[uint(l)]); got != want {
			t.Errorf("%v has %d parameters, PV%d defines %d", l, got, s.ProtocolVersion[0], want)
		}
		if names := len(lang.GetParamNamesForVersion(plutigoVersions[l])); len(ms[uint(l)]) > names {
			t.Errorf("%v has %d parameters, plutigo names only %d", l, len(ms[uint(l)]), names)
		}
	}
	ms[uint(PlutusV3)][0] = -1
	if again, _ := CostModels(s); again[uint(PlutusV3)][0] == -1 {
		t.Fatal("CostModels returns shared storage")
	}
}

// TestGenesisCostModelsAsNodesRead decodes the rendered files the way the
// nodes do. cardano-ledger requires the Alonzo costModels to be PlutusV1
// alone at exactly 166 entries and Conway's plutusV3CostModel at exactly 251
// (cardano-ledger-core Cardano/Ledger/Plutus/CostModels.hs
// guardNumberOfParameters, costModelInitParamCount); the full models ride in
// the Alonzo extraConfig, which is parsed leniently and overrides the
// genesis models when a chain starts in a later era. dingo applies it the
// same way, through gouroboros' ApplyExtraConfigCostModels.
func TestGenesisCostModelsAsNodesRead(t *testing.T) {
	b := render(t, fixedSpec(), 1)
	ag, err := alonzo.NewAlonzoGenesisFromReader(bytes.NewReader(b.Alonzo))
	if err != nil {
		t.Fatal(err)
	}
	if got := ag.CostModels; len(got) != 1 || len(got["PlutusV1"]) != 166 {
		t.Fatalf("alonzo costModels: %d languages, PlutusV1 %d entries", len(got), len(got["PlutusV1"]))
	}
	cg, err := conway.NewConwayGenesisFromReader(bytes.NewReader(b.Conway))
	if err != nil {
		t.Fatal(err)
	}
	if n := len(cg.PlutusV3CostModel); n != conwayV3Len {
		t.Fatalf("conway plutusV3CostModel has %d entries, the ledger requires %d", n, conwayV3Len)
	}
	full, err := CostModels(fixedSpec())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(cg.PlutusV3CostModel, full[uint(PlutusV3)][:conwayV3Len]) {
		t.Fatal("conway plutusV3CostModel is not the prefix of the full PlutusV3 model")
	}
	got, err := ag.ApplyExtraConfigCostModels(map[uint][]int64{uint(PlutusV3): cg.PlutusV3CostModel})
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range Languages() {
		if !slices.Equal(got[uint(l)], full[uint(l)]) {
			t.Errorf("%v after extraConfig: %d entries, want the full %d", l, len(got[uint(l)]), len(full[uint(l)]))
		}
	}
}

func TestShortCostModelNeedsOverride(t *testing.T) {
	s := Default()
	s.CostModelLen[PlutusV3] = 251
	err := Check(s)
	if err == nil || !strings.Contains(err.Error(), "PlutusV3") {
		t.Fatalf("Check = %v, want a short PlutusV3 refusal", err)
	}
	s.Allow.ShortCostModels = true
	if err := Check(s); err != nil {
		t.Fatalf("Check with the override = %v", err)
	}
	ms, err := CostModels(s)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(ms[uint(PlutusV3)]); n != 251 {
		t.Fatalf("truncated PlutusV3 has %d entries", n)
	}
	s.CostModelLen[PlutusV3] = 298
	if err := Check(s); err == nil {
		t.Fatal("Check accepted a cost model longer than the shipped one")
	}
}

var plutigoVersions = [...]lang.LanguageVersion{lang.LanguageVersionV1, lang.LanguageVersionV2, lang.LanguageVersionV3}
