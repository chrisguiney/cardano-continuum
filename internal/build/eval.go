package build

import (
	"errors"
	"fmt"
	"time"

	"github.com/Salvionied/apollo/v2/backend"
	"github.com/Salvionied/apollo/v2/backend/fixed"
	"github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/gouroboros/ledger/common/script"
	"github.com/blinklabs-io/gouroboros/ledger/conway"
	mockledger "github.com/blinklabs-io/ouroboros-mock/ledger"
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

func (b *Builder) chainContext(s spec) *evalContext {
	known := append(append([]common.Utxo(nil), s.sp.Inputs...), s.known...)
	return &evalContext{
		FixedChainContext: fixed.NewFixedChainContext(b.protocolParams(), backend.GenesisParameters{}, b.params.NetworkID),
		b:                 b,
		known:             known,
	}
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

// evaluate runs every redeemer of txCbor against the per-transaction budget
// and returns the units each one used. utxos resolve the inputs, collateral
// and reference inputs.
func (b *Builder) evaluate(txCbor []byte, utxos []common.Utxo) (map[common.RedeemerKey]common.ExUnits, error) {
	if len(b.params.PlutusV3) == 0 {
		return nil, errors.New("build: no PlutusV3 cost model")
	}
	if b.params.SlotLength <= 0 {
		return nil, errors.New("build: slot length unset; script contexts need slot times")
	}
	tx, err := conway.NewConwayTransactionFromCbor(txCbor)
	if err != nil {
		return nil, fmt.Errorf("build: evaluate: decode: %w", err)
	}
	byRef := make(map[string]common.Utxo, len(utxos))
	for _, u := range utxos {
		byRef[u.Id.String()] = u
	}
	start, slot := b.params.SystemStart, b.params.SlotLength
	ls := mockledger.NewLedgerStateBuilder().
		WithNetworkId(uint(b.params.NetworkID)).
		WithUtxoById(func(in common.TransactionInput) (common.Utxo, error) {
			if u, ok := byRef[in.String()]; ok {
				return u, nil
			}
			return common.Utxo{}, fmt.Errorf("build: evaluate: input %s not offered", in)
		}).
		WithSlotToTime(func(s uint64) (time.Time, error) { return start.Add(time.Duration(s) * slot), nil }).
		WithTimeToSlot(func(t time.Time) (uint64, error) { return uint64(t.Sub(start) / slot), nil }).
		Build()
	budget := b.params.MaxTxExUnits
	used, err := script.EvaluatePlutusScripts(tx, ls, b.params.ProtocolMajor,
		map[uint][]int64{2: b.params.PlutusV3}, 3, conway.EraNameConway, &budget)
	if err != nil {
		return nil, fmt.Errorf("build: evaluate: %w", err)
	}
	return used, nil
}
