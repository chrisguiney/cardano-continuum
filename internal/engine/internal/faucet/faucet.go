// Package faucet is the economy's source on a devnet: every Every slots it
// tops up each live wallet holding under half its profile's budget, back to
// the budget, in one fan-out transaction. One round is in flight at a time,
// since the faucet's change is not spendable until the round settles.
//
// The composite orders every call. Faucet emits Refill and Skipped, and
// reserves and starts the round's transaction through Book; the node's
// answer comes back through Result.
package faucet

import (
	"context"
	"fmt"
	"iter"

	"github.com/blinklabs-io/gouroboros/ledger/common"

	"github.com/chrisguiney/cardano-continuum/internal/build"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/wire"
	"github.com/chrisguiney/cardano-continuum/internal/keys"
	"github.com/chrisguiney/cardano-continuum/internal/plan"
	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

const (
	refillKind = "refill"
	// maxRefillOutputs keeps a round well under the maximum transaction
	// size (about 65 bytes per output against 16 KiB).
	maxRefillOutputs = 100
)

// Clock queues the faucet's next round.
type Clock interface {
	Refill(at sched.Time)
}

// Journal is the decision log of the input being handled.
type Journal interface {
	Emit(at sched.Time, kind string, a population.ActorID, body any)
}

// Book is the engine's unfinished intents and the planner's ledger.
type Book interface {
	// Next is the ID the next intent will take; Claim takes it.
	Next() uint64
	Claim(id uint64)
	// Coins are the outputs the intent may spend for the actor.
	Coins(a population.ActorID, id plan.IntentID) []wire.Coin
	// Reserve holds inputs for an intent's first attempt.
	Reserve(ctx context.Context, id uint64, a population.ActorID, tx wire.TxID, inputs []wire.Ref, ttl uint64) error
	// Add records a started intent; Unawait stops waiting on its answer.
	Add(p *wire.Pending)
	Unawait(id uint64)
	// All are the unfinished intents.
	All() iter.Seq[*wire.Pending]
	// Held is what the actor's wallet holds, counting the change of its
	// unfinished payments.
	Held(id population.ActorID) uint64
	// Refuse writes a refill off after its first send was rejected and
	// releases what that kills.
	Refuse(ctx context.Context, at sched.Time, p *wire.Pending, r wire.SubmitResult) error
	// Refusable reports that the answer writes a one-shot intent off.
	Refusable(r wire.SubmitResult) bool
}

// Selector picks the faucet's inputs.
type Selector interface {
	Select(avail []wire.Coin, need uint64) []wire.Coin
}

// Builder builds the fan-out transaction.
type Builder interface {
	FanOut(ctx context.Context, w keys.Wallet, sp build.Spend, outs []build.Output) (build.Built, error)
}

// Wallets are the wallets the faucet tops up.
type Wallets interface {
	// IDs are the actors, ascending.
	IDs() []population.ActorID
	// Account is the actor's address and budget; false when the faucet
	// does not feed it (abandoned, or an adversary spending its own).
	Account(id population.ActorID) (Account, bool)
	// Utxos turns coins of the wallet into the outputs the builder spends.
	Utxos(w keys.Wallet, coins []wire.Coin) []common.Utxo
}

// Account is one wallet the faucet may fund.
type Account struct {
	Address common.Address
	Budget  uint64
}

// TTL is the transaction time-to-live an attempt at at takes.
type TTL interface {
	TTL(at sched.Time, guarded, script bool) uint64
}

// Spec is what a faucet is built from.
type Spec struct {
	// Wallet is the faucet's keys; zero without a faucet.
	Wallet keys.Wallet
	// Actor owns the faucet's outputs in the ledger.
	Actor population.ActorID
	Slots sched.Slots
	// Every is the slots between rounds.
	Every uint64
	// Headroom is what a round holds back for its fee and change.
	Headroom uint64
}

// Env is what the faucet shares with the rest of the engine.
type Env struct {
	Clock    Clock
	Journal  Journal
	Book     Book
	Selector Selector
	Builder  Builder
	Wallets  Wallets
	TTL      TTL
}

// Faucet owns the refill round and its schedule.
type Faucet struct {
	spec Spec
	env  Env
	// at is the next round's time; zero without a faucet.
	at sched.Time
}

// New returns a faucet that does nothing until Start or Load.
func New(sp Spec, env Env) *Faucet { return &Faucet{spec: sp, env: env} }

// Save is the next round's time, the faucet's snapshot section.
func (f *Faucet) Save() sched.Time { return f.at }

// Start queues the first round at time zero.
func (f *Faucet) Start() {
	f.at = 0
	f.env.Clock.Refill(0)
}

// Load queues the round a snapshot saved.
func (f *Faucet) Load(at sched.Time) {
	f.at = at
	f.env.Clock.Refill(at)
}

// schedule queues the faucet's next round after now.
func (f *Faucet) schedule(now sched.Time) {
	f.at = f.spec.Slots.SlotStart(f.spec.Slots.SlotAt(now) + f.spec.Every)
	f.env.Clock.Refill(f.at)
}

// Refill runs one round at time at, then schedules the next.
func (f *Faucet) Refill(ctx context.Context, at sched.Time) error {
	defer f.schedule(at)
	if f.refilling() {
		return nil
	}
	outs, to, amounts := f.needy()
	if len(outs) == 0 {
		return nil
	}
	skip := func(why string) {
		f.env.Journal.Emit(at, wire.KindSkipped, f.spec.Actor, wire.Skipped{Kind: refillKind, Why: why})
	}
	var total uint64
	for _, o := range outs {
		total += o.Amount
	}
	id := f.env.Book.Next()
	coins := f.env.Selector.Select(f.env.Book.Coins(f.spec.Actor, plan.IntentID(id)), total+f.spec.Headroom)
	if coins == nil {
		skip(fmt.Sprintf("faucet cannot cover %d", total+f.spec.Headroom))
		return nil
	}
	inputs := make([]wire.Ref, len(coins))
	for i, c := range coins {
		inputs[i] = c.Ref
	}
	// An overdue round still goes to the node at the Tick's slot.
	ttl := f.env.TTL.TTL(at, false, false)
	built, err := f.env.Builder.FanOut(ctx, f.spec.Wallet, build.Spend{Inputs: f.env.Wallets.Utxos(f.spec.Wallet, coins), TTL: ttl}, outs)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		skip("build: " + err.Error())
		return nil
	}
	if err := f.env.Book.Reserve(ctx, id, f.spec.Actor, wire.TxID(built.ID), inputs, ttl); err != nil {
		skip("reserve: " + err.Error())
		return nil
	}
	f.env.Book.Claim(id)
	f.env.Book.Add(&wire.Pending{ID: id, Actor: uint64(f.spec.Actor), Refill: true, Attempt: wire.Attempt{Tx: wire.TxID(built.ID), TTL: ttl, Cbor: built.Tx, Awaiting: true, Txs: []wire.TxID{wire.TxID(built.ID)}}})
	f.env.Journal.Emit(at, wire.KindRefill, f.spec.Actor, wire.Refill{
		Intent: id, ID: wire.TxID(built.ID), To: to, Amounts: amounts, Fee: built.Fee, TTL: ttl, Inputs: inputs, Tx: built.Tx,
	})
	return nil
}

// refilling reports whether a refill is unfinished.
func (f *Faucet) refilling() bool {
	for p := range f.env.Book.All() {
		if p.Refill {
			return true
		}
	}
	return false
}

// needy lists the live wallets under half their budget, in actor order, with
// what tops each up. Holdings count the change of unfinished payments, so a
// wallet waiting on its own payment is not refilled for it.
func (f *Faucet) needy() ([]build.Output, []uint64, []uint64) {
	var outs []build.Output
	var to, amounts []uint64
	for _, id := range f.env.Wallets.IDs() {
		if len(outs) == maxRefillOutputs {
			break
		}
		acct, ok := f.env.Wallets.Account(id)
		if !ok {
			continue
		}
		held := f.env.Book.Held(id)
		if held >= acct.Budget/2 {
			continue
		}
		outs = append(outs, build.Output{To: acct.Address, Amount: acct.Budget - held})
		to, amounts = append(to, uint64(id)), append(amounts, acct.Budget-held)
	}
	return outs, to, amounts
}

// Result records the node's answer to a refill. Only a first-send
// rejection writes it off; an unreached or resent one stays reserved until
// the ledger settles it or its TTL kills it, as a payment did before it had
// a retry machine.
func (f *Faucet) Result(ctx context.Context, at sched.Time, p *wire.Pending, r wire.SubmitResult) error {
	switch r.Verdict {
	case wire.VerdictAccepted, wire.VerdictUnreached:
	case wire.VerdictRejected:
		if !f.env.Book.Refusable(r) {
			break
		}
		return f.env.Book.Refuse(ctx, at, p, r)
	default:
		return fmt.Errorf("%w: intent %d", wire.ErrUnknownVerdict, r.Intent)
	}
	p.Awaiting, p.Cbor = false, nil
	f.env.Book.Unawait(p.ID)
	return nil
}
