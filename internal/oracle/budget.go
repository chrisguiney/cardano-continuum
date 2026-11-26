package oracle

import (
	"fmt"

	"github.com/blinklabs-io/gouroboros/ledger/common"

	"github.com/chrisguiney/cardano-continuum/internal/chain"
)

// BlockBudget is one applied block's execution units: what its
// transactions' redeemers declare in total, against maxBlockExUnits.
type BlockBudget struct {
	At        chain.Point
	Used, Max common.ExUnits
	Txs       int
}

// checkBudget reports a block over its execution budget. The BBODY rule
// refuses one (cardano-ledger eras/alonzo/impl/src/Cardano/Ledger/Alonzo/
// Rules/Bbody.hs, pointWiseExUnits (<=) txTotal ppMax ?! TooManyExUnits, at
// 226b002d), so a node that forged or adopted it broke consensus. It is
// fatal: such a block also carries every transaction that should never
// have been forged, such as an exunits-block-gap transaction.
func checkBudget(s Snapshot) []Finding {
	b := s.Budget
	if b == nil || (b.Used.Memory <= b.Max.Memory && b.Used.Steps <= b.Max.Steps) {
		return nil
	}
	return []Finding{{
		Kind: BlockOverBudget, Severity: Fatal,
		Summary: fmt.Sprintf("block %s holds %d transactions declaring %d memory and %d steps, over maxBlockExUnits %d / %d",
			pointText(b.At), b.Txs, b.Used.Memory, b.Used.Steps, b.Max.Memory, b.Max.Steps),
		Facts: []Fact{key("block", pointText(b.At)),
			note("memory", fmt.Sprint(b.Used.Memory)), note("steps", fmt.Sprint(b.Used.Steps)),
			note("max_memory", fmt.Sprint(b.Max.Memory)), note("max_steps", fmt.Sprint(b.Max.Steps))},
	}}
}
