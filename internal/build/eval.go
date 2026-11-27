package build

import (
	"github.com/Salvionied/apollo/v2/backend"
	"github.com/Salvionied/apollo/v2/backend/fixed"
	"github.com/blinklabs-io/gouroboros/ledger/common"
)

// evalContext is Apollo's chain context: fixed protocol parameters, no
// chain UTxOs (Apollo spends only what the request offers), and local script
// evaluation. Apollo v2.2.1 has no in-process evaluator, so EvaluateTx runs
// the transaction's scripts with gouroboos' script.EvaluatePlutusScripts,
// which executes them on plutigo's CEK machine with the Params' PlutusV3
// cost model: the same code path the ledger rules (and the fake chain) use.
type evalContext struct {
	*fixed.FixedChainContext
	b     *Builder
	known []common.Utxo
}

// Capabilities reports the fixed context's capabilities plus local
// evaluation over caller-supplied UTxOs.
func (e *evalContext) Capabilities() backend.CapabilitySet {
	return e.FixedChainContext.Capabilities() |
		backend.CapabilitySet(backend.CapabilityEvaluateTx|backend.CapabilityEvaluateTxAdditionalUtxos)
}

// EvaluateTx implements backend.ChainContext.
func (e *evalContext) EvaluateTx(txCbor []byte, extra []common.Utxo) (map[common.RedeemerKey]common.ExUnits, error) {
	return e.b.evaluate(txCbor, append(append([]common.Utxo(nil), e.known...), extra...))
}
