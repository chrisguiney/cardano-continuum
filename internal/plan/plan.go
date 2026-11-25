// Package plan decides which outputs a transaction may spend, so the
// engine's own transactions never collide, never pay an intent twice and
// never build on an output that a rollback took away.
//
// The rules are the reservation store contract of
// models/specs/planner-reservations (README, "The reservation store
// contract"), model-checked in PlannerReservations.tla:
//
//  1. Reservations belong to intents. An output is free or held by one
//     intent; every attempt of that intent may spend it, no other may.
//  2. An attempt holds its inputs from Reserve until it is settled (in a
//     block with K successors in the ledger's view) or dead. Nothing else
//     releases: not a drop notice, not a rollback, not a confirmation short
//     of K deep, not a transport error.
//  3. Dead is any of: an input spent by another transaction in a settled
//     block; the settled tip's slot at or past the TTL with the attempt not
//     settled; refused by a node before it reached any; built on a dead
//     attempt. Death cascades to descendants in the same step.
//  4. A further attempt of an intent must be unable to land together with
//     every unfinished earlier attempt: they, or what they build on, share an
//     input. Comparing with the newest attempt only lets a third attempt
//     built on the first's change land beside the second.
//  5. Reserve commits the record and its reservations before returning, so
//     before the transaction can be sent. Advance and Refused commit the
//     state changes, the releases and the settled point together.
//
// The ledger keeps its own view of the chain: the owned outputs unspent at
// the settled point, and the blocks above it. Only the settled part is
// durable; a restart resumes chain-sync at ResumePoint, the settled point,
// and the blocks above it are replayed. Rebuilding from the origin would
// offer outputs it already settled as spent (PlannerRestartFromOriginBug).
//
//	```mermaid
//	flowchart LR
//	  sync[chain-sync events] -- Advance --> led[Ledger]
//	  sub[submitter] -- Refused / DropNotice --> led
//	  eng[engine] -- Available, Select, Reserve --> led
//	  led -- Commit blob --> disk[(store checkpoint)]
//	```
//
// The pure part (state) never does I/O; Ledger applies each operation to a
// copy, commits its encoding and only then adopts it, so a failed commit
// leaves the ledger as it was.
package plan

import (
	"context"
	"errors"

	"github.com/blinklabs-io/gouroboros/ledger/common"

	"github.com/chrisguiney/cardano-continuum/internal/chain"
	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/store"
)

// Errors returned by the ledger.
var (
	// ErrNotAvailable: an input is not in Available for the intent's actor:
	// held by another intent, spent in the view, or unknown.
	ErrNotAvailable = errors.New("plan: input not available")
	// ErrNotExclusive: the attempt could land together with an unfinished
	// attempt of the same intent, which would pay the intent twice.
	ErrNotExclusive = errors.New("plan: attempt can land beside an earlier attempt")
	// ErrContradictory: the attempt builds on two transactions that cannot
	// both land, so it can never land itself.
	ErrContradictory = errors.New("plan: inputs contradict each other")
	// ErrPaid: the intent already has a settled attempt.
	ErrPaid = errors.New("plan: intent already paid")
	// ErrBadAttempt: the attempt is malformed: no inputs, a repeated input,
	// a known transaction ID, or change not owned by the actor.
	ErrBadAttempt = errors.New("plan: malformed attempt")
	// ErrUnknownTx: the transaction is not an unfinished attempt.
	ErrUnknownTx = errors.New("plan: unknown attempt")
	// ErrNotConnected: a block does not extend the view's tip.
	ErrNotConnected = errors.New("plan: block does not extend the tip")
	// ErrTooDeep: a rollback target is below the settled point or not on the
	// view's chain. The settled point never moves back.
	ErrTooDeep = errors.New("plan: rollback below the settled point")
	// ErrBlob: a stored ledger cannot be read.
	ErrBlob = errors.New("plan: bad stored ledger")
)

// IntentID names one payment the engine wants made. Several attempts
// (transactions) may try to make it; at most one may ever land.
type IntentID uint64

// Ref names a transaction output.
type Ref = chain.Ref

// TxID is a transaction hash.
type TxID = chain.TxID

// Utxo is an output the planner may hand out: whose it is and what it holds.
type Utxo struct {
	Ref      Ref
	Owner    population.ActorID
	Lovelace uint64
}

// Attempt is one transaction trying to make an intent.
type Attempt struct {
	Tx    TxID
	Actor population.ActorID
	// Inputs are what the transaction spends; each must be in Available.
	Inputs []Ref
	// Change are the outputs it pays back to Actor. While the attempt is
	// unfinished they are offered to Actor's other intents (chaining).
	Change []Utxo
	// TTL is the transaction's invalid-hereafter slot: it is valid only in
	// slots below TTL. Zero means none, and such an attempt dies only by
	// conflict or refusal.
	TTL uint64
}

// Pending is an unfinished attempt and its intent.
type Pending struct {
	Intent IntentID
	Attempt
}

// Result is what one step finished.
type Result struct {
	Settled []TxID
	Dead    []TxID
}

// Opts narrows Available.
type Opts struct {
	// Chain also offers the change of the actor's unfinished attempts.
	Chain bool
	// MaxDepth, when positive, withholds change whose spender would have
	// more than MaxDepth unfinished transactions in its ancestry, itself
	// included, as a node's mempool bounds chains of dependent transactions.
	MaxDepth int
}

// Owners says which actor an address belongs to.
type Owners interface {
	Owner(a common.Address) (population.ActorID, bool)
}

// Committer makes the ledger's encoding durable. It returns only once the
// blob would survive a crash.
type Committer interface {
	Commit(ctx context.Context, b store.Blob) error
}

// Discarder is implemented by a Committer that stores nothing, because
// durability lives elsewhere. The ledger then neither encodes its state nor
// keeps a copy to fall back on: a Commit that cannot fail needs neither.
type Discarder interface {
	Discards() bool
}

// Config is fixed for a ledger's lifetime.
type Config struct {
	// K is the security parameter: a block with K successors is settled.
	K      uint64
	Owners Owners
	Store  Committer
}

func (c Config) validate() error {
	if c.K == 0 || c.Owners == nil || c.Store == nil {
		return errors.New("plan: config needs K, Owners and Store")
	}
	return nil
}
