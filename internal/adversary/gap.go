package adversary

import (
	"fmt"

	"github.com/blinklabs-io/gouroboros/ledger/common"

	"github.com/chrisguiney/cardano-continuum/internal/mistake"
	"github.com/chrisguiney/cardano-continuum/internal/submit"
)

// The exunits-block-gap attack. On a genesis whose per-transaction
// execution budget exceeds the per-block one, a transaction can pass every
// admission check and still never fit a block:
//
//   - the ledger's UTXO rule bounds a transaction by maxTxExUnits only
//     (cardano-ledger Alonzo/Rules/Utxo.hs validateExUnitsTooBigUTxO, at
//     226b002d);
//   - the node's mempool measures it against the same per-transaction limit
//     (ouroboros-consensus Shelley/Ledger/Mempool.hs txMeasureAlonzo, at
//     136122cc) and admits it while the mempool, twice a block's capacity
//     (Mempool/Capacity.hs computeMempoolCapacity), has room;
//   - forging takes the longest mempool prefix within one block's capacity
//     (Mempool/Impl/Common.hs, splitAfterTxSize over blockCapacityTxMeasure),
//     so the transaction is never forged and every transaction behind it
//     waits until it expires;
//   - a block holding it breaks the BBODY rule (Alonzo/Rules/Bbody.hs,
//     TooManyExUnits), which an honest node refuses to adopt.
//
// The adversary submits transactions whose declared units sit in that gap
// and whose script really spends them (build.Burn), so no node can tell
// the declaration from the work. Its oracle: such a transaction is never in
// a block, and a block over maxBlockExUnits is fatal (oracle's
// BlockOverBudget). Admission is not judged right or wrong: a node that
// admits it follows the rules cited above, and one that refuses it as too
// big for a block applies a check no rule states. Implementations that
// differ are a verdict split, recorded without a correct answer.

// Budgets are the genesis execution limits an adversary reads.
type Budgets struct {
	Tx, Block common.ExUnits
}

// stepsPerMemory is the steps a builtin-free script spends per memory
// unit: every CEK machine step costs 16000 CPU and 100 memory in the PV10
// cost model. No script spends memory more cheaply, so memory above
// steps/160 can be declared but not spent.
const stepsPerMemory = 160

// gapSlack keeps the target clear of both edges: the burn loop's measured
// memory differs from its size's estimate by a few percent.
const gapSlack = 20

// GapTarget returns the memory a gap transaction declares: midway between
// the block and transaction limits, capped so its steps fit both, and why
// there is none when the gap is closed or the steps budget cannot reach it.
func (b Budgets) GapTarget() (int64, string) {
	if b.Tx.Memory <= b.Block.Memory {
		return 0, fmt.Sprintf("maxTxExUnits memory %d fits maxBlockExUnits memory %d: no gap", b.Tx.Memory, b.Block.Memory)
	}
	steps := min(b.Tx.Steps, b.Block.Steps)
	mem := min((b.Tx.Memory+b.Block.Memory)/2, steps/stepsPerMemory)
	mem -= mem / gapSlack
	if mem <= b.Block.Memory+b.Block.Memory/gapSlack {
		return 0, fmt.Sprintf("steps budget %d spends at most %d memory, not past the block's %d", steps, steps/stepsPerMemory, b.Block.Memory)
	}
	return mem, ""
}

// GapExpect is the verdict a gap transaction's send calls for: admitted,
// or refused as too big (the class consensus reuses for its capacity
// measure). Either way it must never be forged.
func GapExpect() mistake.Expected {
	return mistake.Expected{Accept: true, Classes: []submit.Class{submit.ExUnitsTooBig}}
}

// gapOverrides is the genesis the exunits-block-gap attack needs:
// per-transaction memory of 140M against mainnet's per-block 72M, and
// per-transaction steps raised to the block's 20B so a script can spend
// the memory (stepsPerMemory). The devnet must also allow inconsistent
// ExUnits.
var gapOverrides = []string{"maxTxExUnits.memory=140000000", "maxTxExUnits.steps=20000000000"}

// GenesisOverrides returns the continuum-genesis -override values attack n
// needs, nil for an attack any devnet supports.
func GenesisOverrides(n Name) []string {
	if n == ExUnitsGap {
		return append([]string(nil), gapOverrides...)
	}
	return nil
}

func planGap(s Sight) (Step, string) {
	mem, why := s.Budgets().GapTarget()
	if why != "" {
		return Step{}, why
	}
	return Step{Attack: ExUnitsGap, Memory: mem}, ""
}

// GapCollateral is the collateral a gap transaction declares: 150% of the
// script fee of 140M memory and 20B steps at mainnet prices (8.08 + 1.44
// ADA), rounded up past the size fee.
const GapCollateral = 15_000_000
