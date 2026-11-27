package engine

import (
	"context"
	"fmt"

	"github.com/blinklabs-io/gouroboros/ledger/common"

	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/faucet"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/pay"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/wire"
	"github.com/chrisguiney/cardano-continuum/internal/keys"
	"github.com/chrisguiney/cardano-continuum/internal/plan"
	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

// This file routes the faucet component (internal/engine/internal/faucet)
// into the engine: the planner ledger and the cohort as it reads them.

// faucetLedger is the planner's ledger and the retry bridge as the faucet
// reaches them.
type faucetLedger struct {
	*pay.Pay
	retry *pay.Retry
}

// Held implements faucet.Book.
func (l faucetLedger) Held(id population.ActorID) uint64 {
	var sum uint64
	for _, u := range l.Reads().Available(id, plan.IntentID(l.Next()), plan.Opts{Chain: true}) {
		sum += u.Lovelace
	}
	return sum
}

// Refusable implements faucet.Book.
func (l faucetLedger) Refusable(r wire.SubmitResult) bool { return pay.Refusable(r.Verdict, r.Resend) }

// Refuse implements faucet.Book.
func (l faucetLedger) Refuse(ctx context.Context, at sched.Time, p *wire.Pending, r wire.SubmitResult) error {
	dead, err := l.Refused(ctx, plan.TxID(p.Tx))
	if err != nil {
		return fmt.Errorf("engine: refuse refill %d: %w", p.ID, err)
	}
	return l.retry.Release(ctx, at, dead, pay.Reason(r))
}

// faucetRoster is the cohort and the ledger's view as the faucet reads the
// wallets it feeds.
type faucetRoster struct{ e *Engine }

// IDs implements faucet.Wallets.
func (r faucetRoster) IDs() []population.ActorID { return r.e.co.IDs() }

// Account implements faucet.Wallets.
func (r faucetRoster) Account(id population.ActorID) (faucet.Account, bool) {
	// An adversary spends its own budget, never the faucet's.
	if a, _ := r.e.co.Get(id); a.State == population.Abandoned || r.e.adv.Adversary(id) {
		return faucet.Account{}, false
	}
	st := r.e.co.Rec(id)
	return faucet.Account{Address: st.Wallet.Address(), Budget: uint64(st.Prof.Budget)}, true
}

// Utxos implements faucet.Wallets.
func (r faucetRoster) Utxos(w keys.Wallet, coins []wire.Coin) []common.Utxo {
	return r.e.ledgerUtxos(w, coins)
}

// newFaucetFor builds the engine's faucet and, when the run has one, seats
// its wallet in the cohort.
func newFaucetFor(e *Engine) (*faucet.Faucet, error) {
	var w keys.Wallet
	if e.cfg.Faucet > 0 {
		var err error
		if w, err = faucetWallet(&e.cfg); err != nil {
			return nil, err
		}
		if err := e.co.BindFaucet(w); err != nil {
			return nil, err
		}
	}
	return faucet.New(faucet.Spec{Wallet: w, Actor: faucetActor, Slots: e.cfg.Slots, Every: e.cfg.RefillEvery, Headroom: feeHeadroom},
		faucet.Env{Clock: evsched{&e.clk}, Journal: &e.jr, Book: faucetLedger{e.pay, e.retry}, Selector: e.sel, Builder: e.builder, Wallets: faucetRoster{e}, TTL: e.in}), nil
}
