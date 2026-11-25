package build

import (
	"context"
	"errors"

	"github.com/Salvionied/apollo/v2"
	"github.com/blinklabs-io/gouroboros/ledger/common"

	"github.com/chrisguiney/cardano-continuum/internal/keys"
)

// Output is one payment of a fan-out.
type Output struct {
	To     common.Address
	Amount uint64
}

// FanOut builds and signs one transaction paying every output from w's
// inputs, returning change to w. A faucet refilling many wallets uses it, so
// one confirmation funds them all instead of one wallet per chained spend.
func (b *Builder) FanOut(ctx context.Context, w keys.Wallet, sp Spend, outs []Output) (Built, error) {
	if len(outs) == 0 {
		return Built{}, errors.New("build: fan-out with no outputs")
	}
	var need uint64
	for _, o := range outs {
		need += o.Amount
	}
	return b.finish(ctx, w, spec{
		sp:   sp,
		need: need,
		add: func(a *apollo.Apollo) (*apollo.Apollo, error) {
			for _, o := range outs {
				a = a.PayToAddress(o.To, int64(o.Amount))
			}
			return a, nil
		},
	})
}
