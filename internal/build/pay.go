package build

import (
	"context"

	"github.com/Salvionied/apollo/v2"
	"github.com/blinklabs-io/gouroboros/ledger/babbage"
	"github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/gouroboros/ledger/mary"

	"github.com/chrisguiney/cardano-continuum/internal/keys"
)

// Payment is a simple ADA payment from one wallet to one address.
type Payment struct {
	// Inputs are the UTxOs the builder may spend. The builder spends only
	// these; choosing them is the planner's job.
	Inputs []common.Utxo
	To     common.Address
	Amount uint64
	// TTL is the absolute slot after which the transaction is invalid.
	TTL uint64
	// Tip is paid in fee above the minimum (Spend.Tip).
	Tip uint64
}

// Pay builds and signs a payment, returning change to w's own address.
// Change below min-UTxO is folded into the fee. All Inputs are spent.
func (b *Builder) Pay(ctx context.Context, w keys.Wallet, p Payment) (Built, error) {
	return b.finish(ctx, w, spec{
		sp:   Spend{Inputs: p.Inputs, TTL: p.TTL, Tip: p.Tip},
		need: p.Amount,
		add: func(a *apollo.Apollo) (*apollo.Apollo, error) {
			return a.PayToAddress(p.To, int64(p.Amount)), nil
		},
	})
}

// minUtxoOverhead is the bytes CIP-55 adds to an output's size for its
// input and UTxO map entry (Conway's MinCoinTxOut).
const minUtxoOverhead = 160

// MinPayment is the smallest plain-ADA payment to the address the ledger
// accepts: coinsPerUTxOByte * (160 + size of the output). The size counts
// the coin itself, so it is found by iterating to a fixed point, which a
// CBOR integer's few size steps reach at once. The output is sized in
// Babbage's map form, the larger of the two, so the result is never
// below the ledger's minimum.
func (b *Builder) MinPayment(to common.Address) uint64 {
	coin := uint64(0)
	for range 4 {
		o := babbage.BabbageTransactionOutput{OutputAddress: to, OutputAmount: mary.MaryTransactionOutputValue{Amount: coin}}
		size, err := common.TransactionOutputCborSize(&o)
		if err != nil {
			// An address the builder holds always encodes.
			panic("build: size a payment output: " + err.Error())
		}
		next := b.params.CoinsPerUtxoByte * (minUtxoOverhead + size)
		if next == coin {
			break
		}
		coin = next
	}
	return coin
}
