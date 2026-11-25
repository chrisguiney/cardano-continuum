package engine

import (
	"bytes"
	"cmp"

	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
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
)

// TxID is a transaction hash.
type TxID [32]byte

// Ref names a transaction output.
type Ref struct {
	Tx    TxID   `cbor:"1,keyasint"`
	Index uint32 `cbor:"2,keyasint"`
}

func compareRefs(a, b Ref) int {
	if c := bytes.Compare(a.Tx[:], b.Tx[:]); c != 0 {
		return c
	}
	return cmp.Compare(a.Index, b.Index)
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

// SubmitResult is the body of a SubmitResult input: what the node said to a
// submission.
type SubmitResult struct {
	Intent uint64 `cbor:"1,keyasint"`
	OK     bool   `cbor:"2,keyasint"`
	Reason string `cbor:"3,keyasint,omitempty"`
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

// Submitted is the body of a Submitted decision: a payment was built, its
// inputs reserved, and it is to be submitted. Tx is the signed transaction.
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

// Released is the body of a Released decision: a payment's reservation was
// dropped without the payment confirming.
type Released struct {
	Intent uint64 `cbor:"1,keyasint"`
	Why    string `cbor:"2,keyasint"`
}

// Confirmed is the body of a Confirmed decision: a payment is K deep.
type Confirmed struct {
	Intent uint64 `cbor:"1,keyasint"`
	ID     TxID   `cbor:"2,keyasint"`
}

// snapshotVersion is bumped whenever snapshot's shape changes.
const snapshotVersion = 1

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
	Utxos      []utxoRec           `cbor:"8,keyasint"`
	Pending    []pendingRec        `cbor:"9,keyasint"`
	Fees       uint64              `cbor:"10,keyasint"`
	Foreign    uint64              `cbor:"11,keyasint"`
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

type utxoRec struct {
	Ref      Ref    `cbor:"1,keyasint"`
	Owner    uint64 `cbor:"2,keyasint"`
	Lovelace uint64 `cbor:"3,keyasint"`
}

type pendingRec struct {
	ID     uint64 `cbor:"1,keyasint"`
	Actor  uint64 `cbor:"2,keyasint"`
	Tx     TxID   `cbor:"3,keyasint"`
	Inputs []Ref  `cbor:"4,keyasint"`
	TTL    uint64 `cbor:"5,keyasint"`
	// Cbor is held only while the node's answer is outstanding, so a
	// restart can resubmit it.
	Cbor     []byte `cbor:"6,keyasint"`
	Awaiting bool   `cbor:"7,keyasint"`
}

func samePoint(a, b eventlog.ChainPoint) bool {
	return a.Slot == b.Slot && bytes.Equal(a.Hash, b.Hash)
}
