package engine

import (
	"context"
	"fmt"

	"github.com/chrisguiney/cardano-continuum/internal/build"
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
	e.queue.Push(e.refillAt, qev{kind: evRefill})
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
		e.emit(at, KindSkipped, faucetActor, Skipped{Kind: refillKind, Why: why})
	}
	var total uint64
	for _, o := range outs {
		total += o.Amount
	}
	id := e.nextID + 1
	coins := e.sel.Select(e.coins(faucetActor, plan.IntentID(id)), total+feeHeadroom)
	if coins == nil {
		skip(fmt.Sprintf("faucet cannot cover %d", total+feeHeadroom))
		return nil
	}
	inputs := make([]Ref, len(coins))
	for i, c := range coins {
		inputs[i] = c.Ref
	}
	ttl := e.cfg.Slots.SlotAt(at) + e.cfg.TTL
	built, err := e.builder.FanOut(ctx, e.faucet, build.Spend{Inputs: e.ledgerUtxos(e.faucet, coins), TTL: ttl}, outs)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		skip("build: " + err.Error())
		return nil
	}
	if err := e.led.Reserve(ctx, plan.IntentID(id), attempt(faucetActor, TxID(built.ID), inputs, ttl)); err != nil {
		skip("reserve: " + err.Error())
		return nil
	}
	e.nextID = id
	e.addPending(&pendingRec{ID: id, Actor: uint64(faucetActor), Tx: TxID(built.ID), TTL: ttl, Cbor: built.Tx,
		Awaiting: true, Txs: []TxID{TxID(built.ID)}, Refill: true})
	e.emit(at, KindRefill, faucetActor, Refill{
		Intent: id, ID: TxID(built.ID), To: to, Amounts: amounts, Fee: built.Fee, TTL: ttl, Inputs: inputs, Tx: built.Tx,
	})
	return nil
}

// refilling reports whether a refill is unfinished.
func (e *Engine) refilling() bool {
	for _, p := range e.pending {
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
	for _, id := range e.order {
		if len(outs) == maxRefillOutputs {
			break
		}
		// An adversary spends its own budget, never the faucet's.
		if a, _ := e.census.Get(id); a.State == population.Abandoned || e.isAdversary(id) {
			continue
		}
		st := e.actors[id]
		budget := uint64(st.prof.Budget)
		held := sumUtxos(e.led.Available(id, plan.IntentID(e.nextID+1), plan.Opts{Chain: true}))
		if held >= budget/2 {
			continue
		}
		outs = append(outs, build.Output{To: st.wallet.Address(), Amount: budget - held})
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
func (e *Engine) refillResult(ctx context.Context, at sched.Time, p *pendingRec, r SubmitResult) error {
	switch r.Verdict {
	case VerdictAccepted, VerdictUnreached:
	case VerdictRejected:
		if r.Resend {
			break
		}
		dead, err := e.led.Refused(ctx, plan.TxID(p.Tx))
		if err != nil {
			return fmt.Errorf("engine: refuse refill %d: %w", p.ID, err)
		}
		return e.release(ctx, at, dead, reasonOf(r))
	default:
		return fmt.Errorf("%w: intent %d", ErrUnknownVerdict, r.Intent)
	}
	p.Awaiting, p.Cbor = false, nil
	e.stopAwaiting(p.ID)
	return nil
}
