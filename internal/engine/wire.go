package engine

import (
	"bytes"

	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
	"github.com/chrisguiney/cardano-continuum/internal/submit"
)

// Decision kinds the engine emits. Inputs use the eventlog kinds.
const (
	KindSpawned      = "Spawned"
	KindTransition   = "Transition"
	KindIntentChosen = "IntentChosen"
	KindSubmitted    = "Submitted"
	KindSkipped      = "Skipped"
	KindReleased     = "Released"
	KindConfirmed    = "Confirmed"
	KindRefused      = "Refused"
	KindGaveUp       = "GaveUp"
)

// TxID is a transaction hash.
type TxID [32]byte

// Ref names a transaction output.
type Ref struct {
	Tx    TxID   `cbor:"1,keyasint"`
	Index uint32 `cbor:"2,keyasint"`
}

// TxOut is an output as the engine reads it: who it pays and how much.
type TxOut struct {
	Index    uint32 `cbor:"1,keyasint"`
	Address  []byte `cbor:"2,keyasint"`
	Lovelace uint64 `cbor:"3,keyasint"`
}

// TxDigest is one transaction as the engine reads it from a block: the
// outputs it consumed and produced (collateral ones for an invalid
// transaction) and its fee.
type TxDigest struct {
	ID      TxID    `cbor:"1,keyasint"`
	Inputs  []Ref   `cbor:"2,keyasint"`
	Outputs []TxOut `cbor:"3,keyasint"`
	Fee     uint64  `cbor:"4,keyasint"`
}

// BlockContent is the Content of a BlockApplied input.
type BlockContent struct {
	Txs []TxDigest `cbor:"1,keyasint"`
}

// Verdict is what a submission established, as internal/submit classifies
// a node's answer.
type Verdict uint8

// Verdicts. The zero value is none, so a body without one is refused.
const (
	// VerdictAccepted: a node took the transaction into its mempool.
	VerdictAccepted Verdict = iota + 1
	// VerdictRejected: a node refused it.
	VerdictRejected
	// VerdictUnreached: the answer is unknown (a dial, timeout or broken
	// connection); the transaction may be in a mempool.
	VerdictUnreached
)

// SubmitResult is the body of a SubmitResult input: what the node said to a
// submission.
type SubmitResult struct {
	Intent  uint64  `cbor:"1,keyasint"`
	Verdict Verdict `cbor:"2,keyasint"`
	Reason  string  `cbor:"3,keyasint,omitempty"`
	// Resend marks a rejection of a transaction that may have been sent
	// before, by a process that died before logging the answer. The
	// transaction's own arrival can cause that rejection, so it proves
	// nothing. It is recorded for rejections only, the one verdict it
	// changes, so a resumed accepted submission logs the same record as an
	// uninterrupted one.
	Resend bool `cbor:"4,keyasint,omitempty"`
	// Failures are the classes the node named, for a rejection. The retry
	// machine reads them: after an earlier send only a class that depends on
	// the transaction alone proves the attempt never reached a mempool.
	Failures []string `cbor:"5,keyasint,omitempty"`
}

// Spawned is the body of a Spawned decision.
type Spawned struct {
	Profile string `cbor:"1,keyasint"`
}

// Transition is the body of a Transition decision.
type Transition struct {
	From string `cbor:"1,keyasint"`
	To   string `cbor:"2,keyasint"`
}

// Submitted is the body of a Submitted decision: an attempt at a payment was
// built, its inputs reserved, and it is to be submitted. A payment has one
// Submitted per attempt, all with the same Intent. Tx is the signed
// transaction.
type Submitted struct {
	Intent uint64 `cbor:"1,keyasint"`
	ID     TxID   `cbor:"2,keyasint"`
	To     uint64 `cbor:"3,keyasint"`
	Amount uint64 `cbor:"4,keyasint"`
	Fee    uint64 `cbor:"5,keyasint"`
	TTL    uint64 `cbor:"6,keyasint"`
	Inputs []Ref  `cbor:"7,keyasint"`
	Tx     []byte `cbor:"8,keyasint"`
}

// Skipped is the body of a Skipped decision: an intent the engine chose but
// did not act on.
type Skipped struct {
	Kind string `cbor:"1,keyasint"`
	Why  string `cbor:"2,keyasint"`
}

// Released is the body of a Released decision: a payment has no unfinished
// attempt left and was not confirmed. Why is "expired", "conflict" or
// "gave up".
type Released struct {
	Intent uint64 `cbor:"1,keyasint"`
	Why    string `cbor:"2,keyasint"`
}

// Refused is the body of a Refused decision: every node rejected an attempt
// and the answer proves it never reached a mempool, so the ledger wrote it
// off. The payment itself goes on: the machine builds the next attempt.
type Refused struct {
	Intent uint64 `cbor:"1,keyasint"`
	ID     TxID   `cbor:"2,keyasint"`
	Why    string `cbor:"3,keyasint"`
}

// GaveUp is the body of a GaveUp decision: the actor's patience is spent. No
// reservation changes; the attempts stay unfinished until the ledger settles
// or writes them off.
type GaveUp struct {
	Intent uint64 `cbor:"1,keyasint"`
}

// Confirmed is the body of a Confirmed decision: a payment is K deep.
type Confirmed struct {
	Intent uint64 `cbor:"1,keyasint"`
	ID     TxID   `cbor:"2,keyasint"`
}

// snapshotVersion is bumped whenever snapshot's shape changes.
const snapshotVersion = 3

// logFormat is the version of the engine's input and decision bodies. It is
// part of the log header's genesis fingerprint, so a log written in another
// format is refused at the header rather than failing on its first body.
const logFormat = 3

// snapshot is the engine state in canonical form: every slice sorted, so
// equal states encode to equal bytes.
type snapshot struct {
	Version    uint32              `cbor:"1,keyasint"`
	Now        sched.Time          `cbor:"2,keyasint"`
	NextIntent uint64              `cbor:"3,keyasint"`
	Actors     []actorRec          `cbor:"4,keyasint"`
	Births     []birthRec          `cbor:"5,keyasint"`
	Conf       eventlog.ChainPoint `cbor:"6,keyasint"`
	Window     []blockRec          `cbor:"7,keyasint"`
	Pending    []pendingRec        `cbor:"8,keyasint"`
	Fees       uint64              `cbor:"9,keyasint"`
	Foreign    uint64              `cbor:"10,keyasint"`
	Ledger     ledgerBlob          `cbor:"11,keyasint"`
}

// ledgerBlob is the planner ledger's store.Blob.
type ledgerBlob struct {
	Version uint32 `cbor:"1,keyasint"`
	Data    []byte `cbor:"2,keyasint"`
}

type actorRec struct {
	ID          uint64     `cbor:"1,keyasint"`
	Profile     string     `cbor:"2,keyasint"`
	State       string     `cbor:"3,keyasint"`
	Born        sched.Time `cbor:"4,keyasint"`
	Changed     sched.Time `cbor:"5,keyasint"`
	Frustration float64    `cbor:"6,keyasint"`
	HazardAt    sched.Time `cbor:"7,keyasint"`
	HazardTo    string     `cbor:"8,keyasint"`
	ActAt       sched.Time `cbor:"9,keyasint"`
	Draws       uint64     `cbor:"10,keyasint"`
}

type birthRec struct {
	Profile string     `cbor:"1,keyasint"`
	At      sched.Time `cbor:"2,keyasint"`
	N       uint64     `cbor:"3,keyasint"`
}

type blockRec struct {
	Point eventlog.ChainPoint `cbor:"1,keyasint"`
	Txs   []TxDigest          `cbor:"2,keyasint"`
}

// pendingRec is a payment the ledger holds unfinished. Its inputs and
// reservation are the ledger's.
type pendingRec struct {
	ID    uint64 `cbor:"1,keyasint"`
	Actor uint64 `cbor:"2,keyasint"`
	// Tx and TTL are the current attempt's.
	Tx  TxID   `cbor:"3,keyasint"`
	TTL uint64 `cbor:"4,keyasint"`
	// Cbor is held while the current attempt may still be sent, so a
	// restart or a resend can submit the same bytes.
	Cbor     []byte `cbor:"5,keyasint"`
	Awaiting bool   `cbor:"6,keyasint"`
	// M is the retry machine; To and Amount are what a rebuild pays.
	M      submit.Machine `cbor:"7,keyasint"`
	To     uint64         `cbor:"8,keyasint"`
	Amount uint64         `cbor:"9,keyasint"`
	// Txs are every unfinished attempt of the payment, oldest first. Only
	// the ledger removes one: settled, dead, or refused by every node.
	Txs []TxID `cbor:"10,keyasint"`
	// NotBefore is when the current attempt may next be sent: the backoff.
	NotBefore sched.Time `cbor:"11,keyasint,omitempty"`
}

func samePoint(a, b eventlog.ChainPoint) bool {
	return a.Slot == b.Slot && bytes.Equal(a.Hash, b.Hash)
}
