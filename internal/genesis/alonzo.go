package genesis

import (
	"encoding/json"
	"fmt"
)

// alonzoDoc is the Alonzo genesis, field names as cardano-ledger's
// FromJSON AlonzoGenesis reads them (Cardano/Ledger/Alonzo/Genesis.hs).
// The constants are cardano-cli 11.2.3.1's create-testnet-data values,
// which match mainnet's: prices 577/10000 and 721/10000000.
type alonzoDoc struct {
	CollateralPercentage int                `json:"collateralPercentage"`
	CostModels           map[string][]int64 `json:"costModels"`
	ExecutionPrices      struct {
		PriceMemory json.Number `json:"priceMemory"`
		PriceSteps  json.Number `json:"priceSteps"`
	} `json:"executionPrices"`
	ExtraConfig struct {
		CostModels map[string][]int64 `json:"costModels"`
	} `json:"extraConfig"`
	LovelacePerUTxOWord int     `json:"lovelacePerUTxOWord"`
	MaxBlockExUnits     ExUnits `json:"maxBlockExUnits"`
	MaxCollateralInputs int     `json:"maxCollateralInputs"`
	MaxTxExUnits        ExUnits `json:"maxTxExUnits"`
	MaxValueSize        int     `json:"maxValueSize"`
}

// renderAlonzo writes s's limits and cost models. The ledger parses
// costModels strictly as PlutusV1 alone at its initial 166 parameters
// (FromJSON AlonzoGenesis, parseCostModels False [PlutusV1]), so the full
// models of every language go in extraConfig, which it parses leniently and
// lays over the genesis models when a chain starts in a later era
// (Cardano/Ledger/Alonzo/Transition.hs alonzoInjectCostModels, applied by
// the Conway injectIntoTestState). cardano-ledger at 226b002d.
func renderAlonzo(s Spec) ([]byte, error) {
	ms, err := CostModels(s)
	if err != nil {
		return nil, err
	}
	v1 := ms[uint(PlutusV1)]
	if len(v1) < alonzoV1Len {
		return nil, fmt.Errorf("PlutusV1 cost model has %d entries; Alonzo genesis needs %d", len(v1), alonzoV1Len)
	}
	d := alonzoDoc{
		CollateralPercentage: 150,
		CostModels:           map[string][]int64{PlutusV1.String(): v1[:alonzoV1Len]},
		LovelacePerUTxOWord:  34482,
		MaxBlockExUnits:      s.BlockExUnits,
		MaxCollateralInputs:  3,
		MaxTxExUnits:         s.TxExUnits,
		MaxValueSize:         5000,
	}
	d.ExecutionPrices.PriceMemory, d.ExecutionPrices.PriceSteps = "0.0577", "7.21e-05"
	d.ExtraConfig.CostModels = make(map[string][]int64, numLanguages)
	for _, l := range Languages() {
		d.ExtraConfig.CostModels[l.String()] = ms[uint(l)]
	}
	return marshal(d)
}

// alonzoV1Len is the PlutusV1 length the ledger requires in the Alonzo
// genesis costModels (costModelInitParamCount PlutusV1).
const alonzoV1Len = 166
