package engine

import (
	"context"
	"fmt"

	"github.com/chrisguiney/cardano-continuum/internal/build"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/clock"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/pay"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/wire"
	"github.com/chrisguiney/cardano-continuum/internal/plan"
	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

// The faucet is the economy's source on a devnet: every RefillEvery slots it
// tops up each live wallet holding under half its profile's budget, back to
// the budget, in one fan-out transaction. One round is in flight at a time,
// since the faucet's change is not spendable until the round settles.

const (
	refillKind = "refill"
	// maxRefillOutputs keeps a round well under the maximum transaction
	// size (about 65 bytes per output against 16 KiB).
	maxRefillOutputs = 100
)

// scheduleRefill queues the faucet's next round after now.
func (e *Engine) scheduleRefill(now sched.Time) {
	e.refillAt = e.cfg.Slots.SlotStart(e.cfg.Slots.SlotAt(now) + e.cfg.RefillEvery)
	e.clk.Push(e.refillAt, clock.Event{Kind: clock.Refill})
}

// refill runs one round at time at, then schedules the next.
func (e *Engine) refill(ctx context.Context, at sched.Time) error {
	defer e.scheduleRefill(at)
	if e.refilling() {
		return nil
	}
	outs, to, amounts := e.needy()
	if len(outs) == 0 {
		return nil
	}
	skip := func(why string) {
		e.jr.Emit(at, KindSkipped, faucetActor, Skipped{Kind: refillKind, Why: why})
	}
	var total uint64
	for _, o := range outs {
		total += o.Amount
	}
	id := e.pay.Next()
	coins := e.sel.Select(e.pay.Coins(faucetActor, plan.IntentID(id)), total+feeHeadroom)
	if coins == nil {
		skip(fmt.Sprintf("faucet cannot cover %d", total+feeHeadroom))
		return nil
	}
	inputs := make([]Ref, len(coins))
	for i, c := range coins {
		inputs[i] = c.Ref
	}
	// An overdue round still goes to the node at the Tick's slot.
	ttl := e.in.TTL(at, false, false)
	built, err := e.builder.FanOut(ctx, e.faucet, build.Spend{Inputs: e.ledgerUtxos(e.faucet, coins), TTL: ttl}, outs)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		skip("build: " + err.Error())
		return nil
	}
	if err := e.pay.Reserve(ctx, id, faucetActor, TxID(built.ID), inputs, ttl); err != nil {
		skip("reserve: " + err.Error())
		return nil
	}
	e.pay.Claim(id)
	e.pay.Add(&wire.Pending{ID: id, Actor: uint64(faucetActor), Refill: true, Attempt: wire.Attempt{Tx: TxID(built.ID), TTL: ttl, Cbor: built.Tx, Awaiting: true, Txs: []TxID{TxID(built.ID)}}})
	e.jr.Emit(at, KindRefill, faucetActor, Refill{
		Intent: id, ID: TxID(built.ID), To: to, Amounts: amounts, Fee: built.Fee, TTL: ttl, Inputs: inputs, Tx: built.Tx,
	})
	return nil
}

// refilling reports whether a refill is unfinished.
func (e *Engine) refilling() bool {
	for p := range e.pay.All() {
		if p.Refill {
			return true
		}
	}
	return false
}

// needy lists the live wallets under half their budget, in actor order, with
// what tops each up. Holdings count the change of unfinished payments, so a
// wallet waiting on its own payment is not refilled for it.
func (e *Engine) needy() ([]build.Output, []uint64, []uint64) {
	var outs []build.Output
	var to, amounts []uint64
	for _, id := range e.co.IDs() {
		if len(outs) == maxRefillOutputs {
			break
		}
		// An adversary spends its own budget, never the faucet's.
		if a, _ := e.co.Get(id); a.State == population.Abandoned || e.adv.Adversary(id) {
			continue
		}
		st := e.co.Rec(id)
		budget := uint64(st.Prof.Budget)
		held := sumUtxos(e.pay.Reads().Available(id, plan.IntentID(e.pay.Next()), plan.Opts{Chain: true}))
		if held >= budget/2 {
			continue
		}
		outs = append(outs, build.Output{To: st.Wallet.Address(), Amount: budget - held})
		to, amounts = append(to, uint64(id)), append(amounts, budget-held)
	}
	return outs, to, amounts
}

func sumUtxos(us []plan.Utxo) uint64 {
	var sum uint64
	for _, u := range us {
		sum += u.Lovelace
	}
	return sum
}

// refillResult records the node's answer to a refill. Only a first-send
// rejection writes it off; an unreached or resent one stays reserved until
// the ledger settles it or its TTL kills it, as a payment did before it had
// a retry machine.
func (e *Engine) refillResult(ctx context.Context, at sched.Time, p *wire.Pending, r SubmitResult) error {
	switch r.Verdict {
	case VerdictAccepted, VerdictUnreached:
	case VerdictRejected:
		if !pay.Refusable(r.Verdict, r.Resend) {
			break
		}
		dead, err := e.pay.Refused(ctx, plan.TxID(p.Tx))
		if err != nil {
			return fmt.Errorf("engine: refuse refill %d: %w", p.ID, err)
		}
		return e.retry.Release(ctx, at, dead, pay.Reason(r))
	default:
		return fmt.Errorf("%w: intent %d", ErrUnknownVerdict, r.Intent)
	}
	p.Awaiting, p.Cbor = false, nil
	e.pay.Unawait(p.ID)
	return nil
}
