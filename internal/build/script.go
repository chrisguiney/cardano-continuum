package build

import (
	"cmp"
	"context"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/Salvionied/apollo/v2"
	"github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/gouroboros/ledger/conway"

	"github.com/chrisguiney/cardano-continuum/internal/keys"
)

// Plutus builds the script transactions: locking funds at a script, spending from it, and minting under a policy.
type Plutus struct{ b *Builder }

// Lock pays Amount to Script's address with Datum inline.
type Lock struct {
	Spend
	Script Script
	Amount uint64
	Datum  common.Datum
}

// Unlock spends Locked, an output at Script's address, into the change
// output. Collateral is taken from the offered inputs.
type Unlock struct {
	Spend
	// Script is the validator Locked sits at.
	Script Script
	// Locked is the script output spent.
	Locked common.Utxo
	// Redeemer is the argument the validator is run with.
	Redeemer common.Datum
	// Signers are key hashes listed as required signers, which a validator
	// reads from its context (extra_signatories). The wallet signs with its
	// payment key only, so each must be that key's hash.
	Signers []common.Blake2b224
	// CoSigners are other parties' keys that sign beside the wallet and
	// are listed as required signers: an escrow depositor approving the
	// release its beneficiary submits.
	CoSigners []keys.Signer
	// From is the slot the transaction is valid from, which a validator
	// reads as its validity range's lower bound. Zero leaves it open:
	// Apollo writes no validity start of 0, so slot 0 cannot be a bound.
	From uint64
}

// Mint mints Quantity of Name under Policy into the change output; a
// negative Quantity burns. Collateral is taken from the offered inputs.
type Mint struct {
	Spend
	Policy   Script
	Name     []byte
	Quantity int64
	Redeemer common.Datum
	// Collateral is the total collateral to declare; zero declares
	// collateralAmount. A policy whose declared units cost more than
	// collateralAmount/1.5 in fees needs more.
	Collateral uint64
}

// ErrNoCollateral reports that no offered input can serve as collateral.
var ErrNoCollateral = errors.New("build: no pure-ada input large enough for collateral")

// collateralAmount is the collateral declared (total_collateral); the rest
// of the collateral input comes back in the collateral return, so a phase-2
// failure forfeits only this much. A script fee of a few ADA at the usual
// 150% needs well under it.
const collateralAmount = 5_000_000

// Lock builds the locking payment.
func (f Plutus) Lock(ctx context.Context, w keys.Wallet, l Lock) (Built, error) {
	addr := l.Script.Address(f.b.params.NetworkID)
	return f.b.finish(ctx, w, spec{
		sp: l.Spend, need: l.Amount,
		add: func(a *apollo.Apollo) (*apollo.Apollo, error) {
			return a.PayToContract(addr, &l.Datum, int64(l.Amount)), nil
		},
	})
}

// Unlock builds the spend of a script output, with execution units from
// local evaluation.
func (f Plutus) Unlock(ctx context.Context, w keys.Wallet, u Unlock) (Built, error) {
	s, err := f.unlockSpec(u, nil)
	if err != nil {
		return Built{}, err
	}
	return f.b.finish(ctx, w, s)
}

// unlockSpec builds the spec for u. A non-nil budget declares those units
// instead of evaluating, which only tests use, to build an under-budgeted
// transaction.
func (f Plutus) unlockSpec(u Unlock, budget *common.ExUnits) (spec, error) {
	col, err := collateral(u.Inputs, collateralAmount)
	if err != nil {
		return spec{}, err
	}
	return spec{
		sp: u.Spend, known: []common.Utxo{u.Locked}, signers: u.CoSigners,
		add: func(a *apollo.Apollo) (*apollo.Apollo, error) {
			var eu common.ExUnits
			if budget != nil {
				eu = *budget
				a = a.DisableExecutionUnitsEstimation()
			}
			for _, k := range u.Signers {
				a = a.AddRequiredSigner(k)
			}
			for _, k := range u.CoSigners {
				a = a.AddRequiredSigner(common.Blake2b224(k.KeyHash()))
			}
			if u.From > 0 {
				a = a.SetValidityStart(int64(u.From))
			}
			return a.AttachScript(u.Script.Witness()).CollectFrom(u.Locked, u.Redeemer, eu).AddCollateral(col).SetCollateralAmount(collateralAmount), nil
		},
	}, nil
}

// Mint builds the mint, with execution units from local evaluation.
func (f Plutus) Mint(ctx context.Context, w keys.Wallet, m Mint) (Built, error) {
	s, err := f.mintSpec(m, nil)
	if err != nil {
		return Built{}, err
	}
	return f.b.finish(ctx, w, s)
}

// mintSpec builds the spec for m; budget is as for unlockSpec.
func (f Plutus) mintSpec(m Mint, budget *common.ExUnits) (spec, error) {
	amount := cmp.Or(m.Collateral, collateralAmount)
	col, err := collateral(m.Inputs, amount)
	if err != nil {
		return spec{}, err
	}
	h := m.Policy.Hash()
	unit := apollo.Unit{PolicyId: hex.EncodeToString(h[:]), Name: hex.EncodeToString(m.Name), Quantity: m.Quantity}
	return spec{
		sp: m.Spend, minted: &unit,
		add: func(a *apollo.Apollo) (*apollo.Apollo, error) {
			if budget != nil {
				a = a.DisableExecutionUnitsEstimation()
			}
			return a.AttachScript(m.Policy.Witness()).Mint(unit, &m.Redeemer, budget).AddCollateral(col).SetCollateralAmount(int64(amount)), nil
		},
	}, nil
}

// collateral picks the first offered input holding only ADA and at least
// twice amount: the declared collateral plus room for a collateral return
// above min-UTxO. Offered inputs are in the planner's order, so the choice
// is deterministic.
func collateral(ins []common.Utxo, amount uint64) (common.Utxo, error) {
	for _, u := range ins {
		if u.Output.Assets() == nil && u.Output.Amount().Uint64() >= 2*amount && u.Output.ScriptRef() == nil {
			return u, nil
		}
	}
	return common.Utxo{}, ErrNoCollateral
}

// Declared returns the execution units tx's redeemers declare in total.
func Declared(tx []byte) (common.ExUnits, error) {
	t, err := conway.NewConwayTransactionFromCbor(tx)
	if err != nil {
		return common.ExUnits{}, fmt.Errorf("build: declared units: %w", err)
	}
	var u common.ExUnits
	if w := t.Witnesses(); w != nil && w.Redeemers() != nil {
		for _, r := range w.Redeemers().Iter() {
			u.Memory += r.ExUnits.Memory
			u.Steps += r.ExUnits.Steps
		}
	}
	return u, nil
}
