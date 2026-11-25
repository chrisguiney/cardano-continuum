package genesis

import (
	"encoding/json"
	"fmt"
	"slices"
)

// PlutusV3CostModelLen is the length of the embedded PlutusV3 cost model.
const PlutusV3CostModelLen = 251

var plutusV3 = mustCostModel(plutusV3CostModel)

func mustCostModel(raw []byte) []int64 {
	var m []int64
	if err := json.Unmarshal(raw, &m); err != nil {
		panic("genesis: embedded PlutusV3 cost model: " + err.Error())
	}
	if len(m) != PlutusV3CostModelLen {
		panic(fmt.Sprintf("genesis: embedded PlutusV3 cost model has %d entries, want %d", len(m), PlutusV3CostModelLen))
	}
	return m
}

// PlutusV3CostModel returns the PlutusV3 cost model the Conway genesis
// carries, in ledger parameter order. The slice is a copy.
func PlutusV3CostModel() []int64 { return slices.Clone(plutusV3) }
