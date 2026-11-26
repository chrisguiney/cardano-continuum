package build

import (
	"context"

	"github.com/Salvionied/apollo/v2"
	"github.com/blinklabs-io/gouroboros/ledger/common"

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
