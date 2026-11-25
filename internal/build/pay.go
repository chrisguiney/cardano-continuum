package build

import (
	"context"
	"fmt"

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
}

// Pay builds and signs a payment, returning change to w's own address.
// Change below min-UTxO is folded into the fee. All Inputs are spent.
func (b *Builder) Pay(ctx context.Context, w keys.Wallet, p Payment) (Built, error) {
	var have uint64
	for _, u := range p.Inputs {
		have += u.Output.Amount().Uint64()
	}
	if have < p.Amount {
		return Built{}, &InsufficientError{Have: have, Need: p.Amount, Shortfall: p.Amount - have}
	}
	if p.TTL > 1<<62 {
		return Built{}, fmt.Errorf("build: ttl %d out of range", p.TTL)
	}
	return b.finish(ctx, w, func() *apollo.Apollo {
		a := apollo.New(b.chainContext()).
			SetWallet(walletAdapter{w}).
			SetChangeAddress(w.Address()).
			SetTtl(int64(p.TTL)).
			PayToAddress(p.To, int64(p.Amount))
		for _, u := range p.Inputs {
			a = a.AddInput(u)
		}
		return a
	})
}
