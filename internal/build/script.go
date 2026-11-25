package build

import (
	"context"
	"encoding/hex"
	"errors"

	"github.com/Salvionied/apollo/v2"
	"github.com/blinklabs-io/gouroboros/ledger/common"

	"github.com/chrisguiney/cardano-continuum/internal/keys"
)

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
	Script   Script
	Locked   common.Utxo
	Redeemer common.Datum
}

// Mint mints Quantity of Name under Policy into the change output; a
// negative Quantity burns. Collateral is taken from the offered inputs.
type Mint struct {
	Spend
	Policy   Script
	Name     []byte
	Quantity int64
	Redeemer common.Datum
}

// ErrNoCollateral reports that no offered input can serve as collateral.
var ErrNoCollateral = errors.New("build: no pure-ada input large enough for collateral")

// collateralAmount is the collateral declared (total_collateral); the rest
// of the collateral input comes back in the collateral return, so a phase-2
// failure forfeits only this much. A script fee of a few ADA at the usual
// 150% needs well under it.
const collateralAmount = 5_000_000

// minCollateral is the smallest input taken as collateral: the declared
// amount plus room for a collateral return above min-UTxO.
const minCollateral = 2 * collateralAmount

// Lock builds the locking payment.
func (b *Builder) Lock(ctx context.Context, w keys.Wallet, l Lock) (Built, error) {
	addr := l.Script.Address(b.params.NetworkID)
	return b.finish(ctx, w, spec{
		sp: l.Spend, need: l.Amount,
		add: func(a *apollo.Apollo) (*apollo.Apollo, error) {
			return a.PayToContract(addr, &l.Datum, int64(l.Amount)), nil
		},
	})
}

// Unlock builds the spend of a script output, with execution units from
// local evaluation.
func (b *Builder) Unlock(ctx context.Context, w keys.Wallet, u Unlock) (Built, error) {
	s, err := b.unlockSpec(u, nil)
	if err != nil {
		return Built{}, err
	}
	return b.finish(ctx, w, s)
}

// unlockSpec builds the spec for u. A non-nil budget declares those units
// instead of evaluating, which only tests use, to build an under-budgeted
// transaction.
func (b *Builder) unlockSpec(u Unlock, budget *common.ExUnits) (spec, error) {
	col, err := collateral(u.Inputs)
	if err != nil {
		return spec{}, err
	}
	return spec{
		sp: u.Spend, known: []common.Utxo{u.Locked},
		add: func(a *apollo.Apollo) (*apollo.Apollo, error) {
			var eu common.ExUnits
			if budget != nil {
				eu = *budget
				a = a.DisableExecutionUnitsEstimation()
			}
			return a.AttachScript(u.Script.V3).CollectFrom(u.Locked, u.Redeemer, eu).AddCollateral(col).SetCollateralAmount(collateralAmount), nil
		},
	}, nil
}

// Mint builds the mint, with execution units from local evaluation.
func (b *Builder) Mint(ctx context.Context, w keys.Wallet, m Mint) (Built, error) {
	s, err := b.mintSpec(m, nil)
	if err != nil {
		return Built{}, err
	}
	return b.finish(ctx, w, s)
}

// mintSpec builds the spec for m; budget is as for unlockSpec.
func (b *Builder) mintSpec(m Mint, budget *common.ExUnits) (spec, error) {
	col, err := collateral(m.Inputs)
	if err != nil {
		return spec{}, err
	}
	h := m.Policy.Hash()
	unit := apollo.Unit{PolicyId: hex.EncodeToString(h[:]), Name: hex.EncodeToString(m.Name), Quantity: m.Quantity}
	return spec{
		sp: m.Spend,
		add: func(a *apollo.Apollo) (*apollo.Apollo, error) {
			if budget != nil {
				a = a.DisableExecutionUnitsEstimation()
			}
			return a.AttachScript(m.Policy.V3).Mint(unit, &m.Redeemer, budget).AddCollateral(col).SetCollateralAmount(collateralAmount), nil
		},
	}, nil
}

// collateral picks the first offered input holding only ADA and at least
// minCollateral. Offered inputs are in the planner's order, so the choice is
// deterministic.
func collateral(ins []common.Utxo) (common.Utxo, error) {
	for _, u := range ins {
		if u.Output.Assets() == nil && u.Output.Amount().Uint64() >= minCollateral && u.Output.ScriptRef() == nil {
			return u, nil
		}
	}
	return common.Utxo{}, ErrNoCollateral
}
