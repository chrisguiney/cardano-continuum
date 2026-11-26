package genesis

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"slices"
)

// Language is a Plutus ledger language. Its value is the ledger's language
// index, the key of a protocol-parameter cost-model map.
type Language uint8

// The ledger languages.
const (
	PlutusV1 Language = iota
	PlutusV2
	PlutusV3
	numLanguages
)

func (l Language) String() string {
	if l < numLanguages {
		return fmt.Sprintf("PlutusV%d", l+1)
	}
	return fmt.Sprintf("Language(%d)", l)
}

// Languages returns every ledger language in index order.
func Languages() []Language { return []Language{PlutusV1, PlutusV2, PlutusV3} }

// paramCounts is, per protocol major version, how many cost-model
// parameters each language defines: the index past the last parameter of a
// builtin the version enables. From the name lists at plutus 1.72.0.0
// (plutus-ledger-api/src/PlutusLedgerApi/V{1,2,3}/ParamName.hs, whose
// constructor order is the ledger order) and the availability table in
// PlutusLedgerApi/Common/Versions.hs: at PV10 (plominPV) V1 keeps its
// original 166, V2 gains integerToByteString and byteStringToInteger
// (indices 175-184) and V3 gains batch 5 (indices 251-296).
var paramCounts = map[uint][numLanguages]int{
	10: {166, 185, 297},
}

// ParamCount returns how many cost-model parameters language l defines at
// protocol major version major, and false for a version with no shipped
// cost models.
func ParamCount(l Language, major uint) (int, bool) {
	c, ok := paramCounts[major]
	if !ok || l >= numLanguages {
		return 0, false
	}
	return c[l], true
}

// costModelsPV10 holds full PV10 cost models in ledger parameter order. The
// values are mainnet's as in force at PV10 (Parquet tables param_update and
// param_update_cost_model, mithril snapshot of epoch 670):
//
//   - PlutusV1 and PlutusV2 positions 0-174: the update in transaction
//     9ba6a580bceb8f94e65a683e8291c89382835f46e3cf928eb521f5581ade4820
//     (epoch 506); mainnet changed neither before PV11.
//   - PlutusV3: the Plomin parameter change in transaction
//     b2a591ac219ce6dcca5847e0248015209c7cb0436aa6bd6863d0c1f152a60bc5
//     (proposed epoch 519). It was enacted: the parameter change proposed in
//     c21b00f90f18fce4... names it as its previous action, and that change's
//     16.5M per-transaction memory is in force from epoch 614.
//   - PlutusV2 positions 175-184 (batch 4b): mainnet never set them at
//     PV10. They are the same builtins' parameters from the PlutusV3 model
//     above, and equal the values mainnet's PV11 PlutusV2 model
//     (c82f3834898e4d70..., epoch 633) puts at those positions.
//
//go:embed template/cost-models-pv10.json
var costModelsPV10 []byte

// conwayV3Len is the length cardano-ledger requires of the Conway genesis
// plutusV3CostModel: it parses that field strictly, failing any count other
// than costModelInitParamCount PlutusV3 (cardano-ledger-conway
// Cardano/Ledger/Conway/PParams.hs, parseCostModelAsArray False, at
// cardano-ledger 226b002d). Longer models go in the Alonzo extraConfig.
const conwayV3Len = 251

// fullModels is the shipped cost models by protocol major version.
var fullModels = map[uint][numLanguages][]int64{10: mustCostModels(costModelsPV10, 10)}

func mustCostModels(raw []byte, major uint) [numLanguages][]int64 {
	var doc map[string][]int64
	if err := json.Unmarshal(raw, &doc); err != nil {
		panic("genesis: embedded cost models: " + err.Error())
	}
	var out [numLanguages][]int64
	for _, l := range Languages() {
		m := doc[l.String()]
		if n, _ := ParamCount(l, major); len(m) != n {
			panic(fmt.Sprintf("genesis: embedded PV%d %v cost model has %d entries, want %d", major, l, len(m), n))
		}
		out[l] = m
	}
	return out
}

// CostModels returns the cost models the genesis carries for s's protocol
// version, keyed by ledger language index, each truncated to
// s.CostModelLen where that is set. The slices are copies.
func CostModels(s Spec) (map[uint][]int64, error) {
	major := s.ProtocolVersion[0]
	full, ok := fullModels[major]
	if !ok {
		return nil, fmt.Errorf("no cost models shipped for protocol version %d", major)
	}
	out := make(map[uint][]int64, numLanguages)
	for _, l := range Languages() {
		m := full[l]
		if n := s.CostModelLen[l]; n > 0 && n <= len(m) {
			m = m[:n]
		}
		out[uint(l)] = slices.Clone(m)
	}
	return out, nil
}

// checkCostModels refuses a truncation the shipped models cannot meet, and
// one shorter than the protocol version defines unless Allow says it is
// deliberate: plutigo and the reference evaluator cost every missing
// parameter at maxBound, so the builtins it prices can never run.
func checkCostModels(s Spec) error {
	major := s.ProtocolVersion[0]
	if _, ok := fullModels[major]; !ok {
		return fmt.Errorf("protocol version %d has no shipped cost models", major)
	}
	for _, l := range Languages() {
		n := s.CostModelLen[l]
		want, _ := ParamCount(l, major)
		switch {
		case n == 0:
		case n < 0 || n > want:
			return fmt.Errorf("%v cost model length %d outside 1..%d", l, n, want)
		case n < want && !s.Allow.ShortCostModels:
			return fmt.Errorf("%v cost model has %d of the %d parameters PV%d defines; allow short cost models to run it anyway", l, n, want, major)
		}
	}
	return nil
}
