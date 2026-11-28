package build

import (
	"context"
	"encoding/hex"
	"errors"

	"github.com/Salvionied/apollo/v2"
	"github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/plutigo/data"

	"github.com/chrisguiney/cardano-continuum/internal/contract"
	"github.com/chrisguiney/cardano-continuum/internal/keys"
)

// The market template (contracts/templates/validators/market.ak): one output
// holding a constant-product market of ADA and the FreeMint token,
// owned by its liquidity provider. A trade spends the pool and pays the
// next pool back with the reserves its datum names; tokens a seller sells
// are minted under FreeMint in the same transaction, so a trade never
// needs the seller's own token outputs.

// Markets builds the market transactions: opening and providing to the
// market, and trading at it.
type Markets struct{ b *Builder }

// feeNum/feeDen is what of a trade's input counts towards its output: a
// 0.3% fee that stays in the pool.
const (
	feeNum = 997
	feeDen = 1000
)

// ErrMarketDrained reports a trade the pool's reserves cannot fill.
var ErrMarketDrained = errors.New("build: the pool cannot fill the trade")

// Trade swaps against Pool, whose datum is Datum: Buy pays Amount lovelace
// for tokens, which go to the change; a sale mints Amount tokens and
// sells them for lovelace.
type Trade struct {
	Spend
	// Pool is the pool output spent and Datum its datum.
	Pool  common.Utxo
	Datum contract.MarketDatum
	// Buy pays lovelace for tokens; false sells tokens for lovelace.
	Buy bool
	// Amount is the lovelace a buy pays or the tokens a sale sells.
	Amount uint64
}

// MarketOpen creates a pool owned by the wallet: Ada lovelace and Tokens of
// the FreeMint token, minted in the same transaction. Provide adds them
// to Pool, whose datum is Datum, instead.
type MarketOpen struct {
	Spend
	// Ada and Tokens are what the provider adds.
	Ada, Tokens uint64
	// Pool and Datum are the pool provided to; nil Pool opens a new one.
	Pool  *common.Utxo
	Datum contract.MarketDatum
}

// Fill is a trade's effect on the pool: the next datum and what the
// trader gets out (tokens for a buy, lovelace for a sale). It is the
// constant-product rule with the fee: out = y*dx*997 / (x*1000 + dx*997),
// rounded down so the reserves' product never falls.
func Fill(d contract.MarketDatum, buy bool, amount uint64) (contract.MarketDatum, uint64, error) {
	x, y := d.Ada, d.Tokens
	if !buy {
		x, y = d.Tokens, d.Ada
	}

	in := int64(amount)
	if in <= 0 || x <= 0 || y <= 0 {
		return contract.MarketDatum{}, 0, ErrMarketDrained
	}

	out := y * in * feeNum / (x*feeDen + in*feeNum)
	if out <= 0 || out >= y {
		return contract.MarketDatum{}, 0, ErrMarketDrained
	}

	next := d
	if buy {
		next.Ada, next.Tokens = d.Ada+in, d.Tokens-out
	} else {
		next.Ada, next.Tokens = d.Ada-out, d.Tokens+in
	}
	return next, uint64(out), nil
}

// MarketDatumOf is the pool datum u carries, if it carries one that its
// value holds: the pool validator refuses to spend any other.
func MarketDatumOf(u common.Utxo) (contract.MarketDatum, bool) {
	dt := u.Output.Datum()
	if dt == nil {
		return contract.MarketDatum{}, false
	}

	d, ok := contract.ParseMarketDatum(dt.Data)
	if !ok || u.Output.Amount().Uint64() < uint64(max(d.Ada, 0)) {
		return contract.MarketDatum{}, false
	}

	var held uint64
	if as := u.Output.Assets(); as != nil && len(d.Policy) == len(common.Blake2b224{}) {
		if q := as.Asset(common.Blake2b224(d.Policy), d.Name); q != nil {
			held = q.Uint64()
		}
	}
	return d, held == uint64(max(d.Tokens, 0))
}

// Trade builds the swap, with execution units from local evaluation.
func (f Markets) Trade(ctx context.Context, w keys.Wallet, t Trade) (Built, error) {
	next, out, err := Fill(t.Datum, t.Buy, t.Amount)
	if err != nil {
		return Built{}, err
	}

	col, err := collateral(t.Inputs, collateralAmount)
	if err != nil {
		return Built{}, err
	}

	need := uint64(0)
	if t.Buy {
		need = t.Amount
	}
	return f.b.finish(ctx, w, spec{
		sp: t.Spend, need: need, known: []common.Utxo{t.Pool},
		add: func(a *apollo.Apollo) (*apollo.Apollo, error) {
			a = a.AttachScript(Market.Witness()).
				CollectFrom(t.Pool, common.Datum{Data: contract.Trade.Data()}, common.ExUnits{})
			a = f.payMarket(a, next)
			if t.Buy {
				// Paid apart, the tokens bought leave the change pure ADA,
				// which a later script transaction takes collateral from.
				a = a.PayToAddress(w.Address(), 0, marketUnit(t.Datum, int64(out)))
			}
			if !t.Buy {
				a = a.AttachScript(FreeMint.Witness()).Mint(marketUnit(t.Datum, int64(t.Amount)), &common.Datum{Data: data.NewConstr(0)}, nil)
			}
			return a.AddCollateral(col).SetCollateralAmount(collateralAmount), nil
		},
	})
}

// OpenMarket opens or provides to a pool, minting the tokens it adds.
func (f Markets) OpenMarket(ctx context.Context, w keys.Wallet, o MarketOpen) (Built, error) {
	col, err := collateral(o.Inputs, collateralAmount)
	if err != nil {
		return Built{}, err
	}

	me := w.PaymentKeyHash()
	next := contract.MarketDatum{Owner: me[:], Policy: FreeMint.Hash().Bytes(), Name: MarketToken, Ada: int64(o.Ada), Tokens: int64(o.Tokens)}
	var known []common.Utxo
	if o.Pool != nil {
		next = o.Datum
		next.Ada, next.Tokens = o.Datum.Ada+int64(o.Ada), o.Datum.Tokens+int64(o.Tokens)
		known = []common.Utxo{*o.Pool}
	}

	return f.b.finish(ctx, w, spec{
		sp: o.Spend, need: o.Ada, known: known,
		add: func(a *apollo.Apollo) (*apollo.Apollo, error) {
			if o.Pool != nil {
				a = a.AttachScript(Market.Witness()).
					CollectFrom(*o.Pool, common.Datum{Data: contract.Provide.Data()}, common.ExUnits{}).
					AddRequiredSigner(common.Blake2b224(me))
			}
			a = f.payMarket(a, next)
			a = a.AttachScript(FreeMint.Witness()).Mint(marketUnit(next, int64(o.Tokens)), &common.Datum{Data: data.NewConstr(0)}, nil)
			return a.AddCollateral(col).SetCollateralAmount(collateralAmount), nil
		},
	})
}

// payMarket pays the pool the reserves d names, with d inline.
func (f Markets) payMarket(a *apollo.Apollo, d contract.MarketDatum) *apollo.Apollo {
	dt := common.Datum{Data: d.Data()}
	return a.PayToContract(Market.Address(f.b.params.NetworkID), &dt, d.Ada, marketUnit(d, d.Tokens))
}

// MarketToken is the name of the token every pool trades: the FreeMint
// token the engine's mints make.
var MarketToken = []byte("continuum")

func marketUnit(d contract.MarketDatum, quantity int64) apollo.Unit {
	return apollo.Unit{PolicyId: hex.EncodeToString(d.Policy), Name: hex.EncodeToString(d.Name), Quantity: quantity}
}
