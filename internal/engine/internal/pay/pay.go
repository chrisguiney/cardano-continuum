// Package pay is the engine's in-flight intents: the bridge to the
// planner's ledger (internal/plan) and the submit-retry machine
// (internal/submit) that drives each intent's attempts.
//
//	Book   the unfinished intents, by ID and by attempt, and the send queue
//	Pay    the ledger bridge: offers, reservations, chain events, deposits
//	Retry  each intent's retry machine, fed the node's answers and the chain
//
// Pay is the only engine component that calls plan.Ledger, and Retry the
// only one that steps a submit.Machine. The composite orders every call, so
// the order of emissions and of the actors' draws is the caller's.
//
// # Contracts
//
// models/specs/planner-reservations, actions:
//
//	Offer(i)          Pay.Coins, Reads.Available
//	Build(i, ins)     Pay.Reserve, the only path to plan.Ledger.Reserve: an
//	                  attempt conflicts with every unfinished one of its intent
//	Persist(t)        Reserve's commit; the composite's ledger store makes the
//	                  logged input the record, before the Submitted decision
//	Deliver(t)        Retry.Answer
//	SubmitFails(t)    Retry.refuse and Pay.Refused: written off only when no
//	                  node can have the attempt
//	ObserveDrop(t)    Retry.Dropped: a drop notice is a hint and releases nothing
//	Process           Pay.Advance, then Retry.Settle and Retry.Release: an
//	                  intent is forgotten only when settled or every attempt is
//	                  dead (Book.drop has no other caller)
//	Restart           Pay.Open, with the Book restored from the snapshot
//
// The chain actions (Extend, IncludeTx, AdvSpend, Rollback, MempoolDrop)
// arrive as the chain events Pay.Advance passes on.
//
// models/specs/submit-retry, actions:
//
//	Build(i, g)       Retry.rebuild through Pay.Reserve; the first attempt is
//	                  the composite's, then Retry.Begin
//	Submit(i)         a SendTx action puts the intent on Book.InFlight, the
//	                  composite's effect queue
//	Classify(i, d)    Retry.Answer and the Diagnose action (Retry.diagnoser)
//	Confirm(i)        Retry.InBlock
//	RolledBack(i)     Retry.LeftView: the counter and fixes stay on the machine
//	Lost(i)           Retry.Release (submit.Dead)
//	Dropped(i)        Retry.Dropped
//	Done(i)           Retry.Settle
//	GiveUp(i)         the GiveUp action: it stops sending and releases nothing
//	Restart           not an input: the Runner marks interrupted sends Resend,
//	                  and the machine (counter, fixes, Sent) is in the snapshot
//
// Refusal(t, v) is decided in exactly one place for a machine-driven intent,
// submit.Step, which Retry.refuse only carries out; a one-shot intent (an
// attack or a refill) has no machine and refuses through Refusable.
package pay

import (
	"context"
	"fmt"
	"strings"

	"github.com/chrisguiney/cardano-continuum/internal/chain"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/wire"
	"github.com/chrisguiney/cardano-continuum/internal/plan"
	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/store"
	"github.com/chrisguiney/cardano-continuum/internal/submit"
)

// ErrUnknownVerdict: a SubmitResult whose verdict is missing or unknown.
var ErrUnknownVerdict = wire.ErrUnknownVerdict

// Pay is the engine's bridge to the planner's ledger: which outputs exist,
// which are reserved, and when an attempt is settled or dead. It holds the
// unfinished intents beside it.
type Pay struct {
	Book
	led    *plan.Ledger
	nextID uint64
	// deposits is the lovelace the engine's settled certificates and
	// proposals hold in the ledger's deposit pot.
	deposits uint64
}

// New returns a bridge with no ledger; Genesis or Open gives it one.
func New() *Pay { return &Pay{Book: newBook()} }

// Genesis starts the ledger on the genesis outputs.
func (pa *Pay) Genesis(cfg plan.Config, utxos []plan.Utxo) error {
	led, err := plan.New(cfg, utxos)
	pa.led = led
	return err
}

// Saved is the bridge's section of a snapshot: the counters, the ledger's
// blob, and the unfinished intents, ascending by ID.
type Saved struct {
	NextID, Deposits uint64
	Ledger           store.Blob
	Pending          []*wire.Pending
}

// Save is the bridge's snapshot section. The intents are the book's own.
func (pa *Pay) Save() (Saved, error) {
	blob, err := pa.led.Blob()
	if err != nil {
		return Saved{}, err
	}
	sv := Saved{NextID: pa.nextID, Deposits: pa.deposits, Ledger: blob}
	for _, id := range pa.IDs() {
		sv.Pending = append(sv.Pending, pa.Get(id))
	}
	return sv, nil
}

// Load opens the ledger a Save recorded, with its counters. It leaves
// sv.Pending to Retry.Load, which the composite calls once the volatile
// window has been fed to the ledger again.
func (pa *Pay) Load(cfg plan.Config, sv Saved) error {
	led, err := plan.Open(cfg, sv.Ledger)
	if err != nil {
		return err
	}
	pa.led, pa.nextID, pa.deposits = led, sv.NextID, sv.Deposits
	return nil
}

// Next is the ID the next intent will get.
func (pa *Pay) Next() uint64 { return pa.nextID + 1 }

// Claim takes id, from Next, for an intent whose first attempt is reserved.
func (pa *Pay) Claim(id uint64) { pa.nextID = id }

// Reserve reserves an attempt of intent id; the ledger refuses one that
// does not conflict with every unfinished attempt of the intent.
func (pa *Pay) Reserve(ctx context.Context, id uint64, a population.ActorID, tx wire.TxID, inputs []wire.Ref, ttl uint64) error {
	return pa.led.Reserve(ctx, plan.IntentID(id), attempt(a, tx, inputs, ttl))
}

// Refused writes off an attempt every node rejected before it arrived and
// returns the attempts that died with it.
func (pa *Pay) Refused(ctx context.Context, tx plan.TxID) ([]plan.TxID, error) {
	return pa.led.Refused(ctx, tx)
}

// Advance feeds the ledger a chain event.
func (pa *Pay) Advance(ctx context.Context, ev chain.Event) (plan.Result, error) {
	return pa.led.Advance(ctx, ev)
}

// Coins lists what the ledger offers actor a for intent id, sorted by Ref.
func (pa *Pay) Coins(a population.ActorID, id plan.IntentID) []wire.Coin {
	us := pa.led.Available(a, id, plan.Opts{})
	out := make([]wire.Coin, len(us))
	for i, u := range us {
		out[i] = wire.Coin{Ref: wire.Ref{Tx: wire.TxID(u.Ref.Tx), Index: u.Ref.Index}, Lovelace: u.Lovelace}
	}
	return out
}

// Spendable is the lovelace the ledger would offer the actor's next
// payment.
func (pa *Pay) Spendable(id uint64) uint64 {
	return SumCoins(pa.Coins(population.ActorID(id), plan.IntentID(pa.nextID+1)))
}

// Balances returns each actor's settled lovelace, reserved or not, by
// actor ID.
func (pa *Pay) Balances() map[uint64]uint64 {
	out := map[uint64]uint64{}
	for _, u := range pa.led.Settled() {
		out[uint64(u.Owner)] += u.Lovelace
	}
	return out
}

// Supply is the settled outputs' lovelace and the deposits held.
func (pa *Pay) Supply() uint64 {
	var sum uint64
	for _, u := range pa.led.Settled() {
		sum += u.Lovelace
	}
	return sum + pa.deposits
}

// Deposit accounts for a settled attempt's deposit: paid when d is
// positive, refunded when negative. It returns the part of a refund that
// comes from outside the population.
func (pa *Pay) Deposit(d int64) uint64 {
	switch {
	case d >= 0:
		pa.deposits += uint64(d)
	case uint64(-d) <= pa.deposits:
		pa.deposits -= uint64(-d)
	default:
		// A refund of a deposit paid before the engine's genesis (a
		// genesis registration) comes from outside the population.
		out := uint64(-d) - pa.deposits
		pa.deposits = 0
		return out
	}
	return 0
}

// Blob is the ledger's state, for store.Checkpoint.Reservations.
func (pa *Pay) Blob() (store.Blob, error) { return pa.led.Blob() }

// Reads is the ledger's read side.
func (pa *Pay) Reads() Reads { return Reads{pa.led} }

// Reads reads the ledger without changing it.
type Reads struct{ led *plan.Ledger }

// Settled implements oracle.Holders.
func (r Reads) Settled() []plan.Utxo { return r.led.Settled() }

// Unfinished implements oracle.Holders.
func (r Reads) Unfinished() []plan.Pending { return r.led.Unfinished() }

// Holder implements oracle.Holders.
func (r Reads) Holder(ref plan.Ref) (plan.IntentID, bool) { return r.led.Holder(ref) }

// Available is what the ledger offers actor a for intent id.
func (r Reads) Available(a population.ActorID, id plan.IntentID, o plan.Opts) []plan.Utxo {
	return r.led.Available(a, id, o)
}

// ResumePoint is the ledger's settled point.
func (r Reads) ResumePoint() chain.Point { return r.led.ResumePoint() }

// Outcome rebuilds the node's answer from the logged result.
func Outcome(r wire.SubmitResult) (submit.Outcome, error) {
	switch r.Verdict {
	case wire.VerdictAccepted:
		return submit.Outcome{Verdict: submit.Accepted}, nil
	case wire.VerdictUnreached, wire.VerdictDropped:
		return submit.Outcome{Verdict: submit.Unreached}, nil
	case wire.VerdictRejected:
		o := submit.Outcome{Verdict: submit.Rejected}
		for _, c := range r.Failures {
			o.Failures = append(o.Failures, submit.Failure{Class: submit.Class(c)})
		}
		return o, nil
	}
	return submit.Outcome{}, fmt.Errorf("%w: intent %d", ErrUnknownVerdict, r.Intent)
}

// Reason joins the failure classes of a rejection for the Refused record.
func Reason(r wire.SubmitResult) string {
	if r.Reason != "" {
		return r.Reason
	}
	return "rejected: " + strings.Join(r.Failures, ", ")
}

// Refusable reports whether a one-shot intent's answer v proves its
// attempt never reached a mempool: a rejection of a first send. A rejection
// of a resend proves nothing, since the attempt's own arrival can cause it
// (RetryRebuildOnOwnSpendBug in models/specs/submit-retry).
func Refusable(v wire.Verdict, resend bool) bool {
	return v == wire.VerdictRejected && !resend
}

// SumCoins is the coins' lovelace.
func SumCoins(cs []wire.Coin) uint64 { return wire.SumCoins(cs) }

func attempt(a population.ActorID, tx wire.TxID, inputs []wire.Ref, ttl uint64) plan.Attempt {
	ins := make([]plan.Ref, len(inputs))
	for i, r := range inputs {
		ins[i] = plan.Ref{Tx: plan.TxID(r.Tx), Index: r.Index}
	}
	return plan.Attempt{Tx: plan.TxID(tx), Actor: a, Inputs: ins, TTL: ttl}
}
