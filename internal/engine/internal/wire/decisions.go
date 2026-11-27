package wire

import (
	"github.com/chrisguiney/cardano-continuum/internal/mistake"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

// Deployed is the body of a Deployed decision: contract Contract
// (build.Contract) is open for locks. Address names its script.
type Deployed struct {
	Contract uint64 `cbor:"1,keyasint"`
	Address  string `cbor:"2,keyasint"`
}

// Controlled is the body of a Controlled decision: a Control input was
// applied, or not when Why says why.
type Controlled struct {
	RequestID string `cbor:"1,keyasint"`
	Op        string `cbor:"2,keyasint"`
	Scenario  string `cbor:"3,keyasint,omitempty"`
	Why       string `cbor:"4,keyasint,omitempty"`
}

// ImageChange is the body of an ImageChange decision: an upgrade wave gave
// the pool actor image Ref.
type ImageChange struct {
	Ref string `cbor:"1,keyasint"`
}

// Guarded is the body of a Guarded decision: the stake-floor guard refused
// an intent. Target is the pool a delegation names.
type Guarded struct {
	Kind   string `cbor:"1,keyasint"`
	Target uint64 `cbor:"2,keyasint,omitempty"`
	Why    string `cbor:"3,keyasint"`
}

// PoolTier is the body of a PoolTier decision: the engine plans the pool
// actor dark (not forging) or live. Why is "agenda" or "recovery".
type PoolTier struct {
	Dark bool   `cbor:"1,keyasint"`
	Why  string `cbor:"2,keyasint"`
}

// Repair is the body of a Repair decision: the pool's pod is to be
// rescheduled or given a new operational certificate.
type Repair struct{}

// Incident is the body of an Incident decision. Kind "stall": epoch Epoch
// began with the ledger's last transition at Label, so an epoch passed
// with no block and the run stops.
type Incident struct {
	Kind  string `cbor:"1,keyasint"`
	Epoch uint64 `cbor:"2,keyasint"`
	Label uint64 `cbor:"3,keyasint"`
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
	// Kind is the intent kind; empty for a payment. To is then the pool,
	// DRep or nobody the intent names, Amount its quantity.
	Kind string `cbor:"9,keyasint,omitempty"`
	// Mistakes are the honest mistakes the attempt carries.
	Mistakes mistake.KindSet `cbor:"10,keyasint,omitempty"`
	// Attack names the attack an adversary's attempt makes, or "cover"
	// for its ordinary traffic; empty for an honest actor.
	Attack string `cbor:"11,keyasint,omitempty"`
	// Action is a proposal's governance action type, Contract the
	// contract a lock or unlock calls.
	Action   string `cbor:"12,keyasint,omitempty"`
	Contract uint64 `cbor:"13,keyasint,omitempty"`
	// A vote's proposal ("txhash#index"), choice (yes, no or abstain),
	// voter class (drep, spo or committee) and voting power: the lovelace
	// the engine's chain view puts behind the voter when it votes.
	Proposal string `cbor:"14,keyasint,omitempty"`
	Choice   string `cbor:"15,keyasint,omitempty"`
	Voter    string `cbor:"16,keyasint,omitempty"`
	Power    uint64 `cbor:"17,keyasint,omitempty"`
}

// Refill is the body of a Refill decision: the faucet pays To[i]
// Amounts[i] in one transaction, whose inputs are reserved like a
// payment's. A refill has no retry machine: a refused one is released and
// the next round is its retry.
type Refill struct {
	Intent  uint64   `cbor:"1,keyasint"`
	ID      TxID     `cbor:"2,keyasint"`
	To      []uint64 `cbor:"3,keyasint"`
	Amounts []uint64 `cbor:"4,keyasint"`
	Fee     uint64   `cbor:"5,keyasint"`
	TTL     uint64   `cbor:"6,keyasint"`
	Inputs  []Ref    `cbor:"7,keyasint"`
	Tx      []byte   `cbor:"8,keyasint"`
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

// Confirmed is the body of a Confirmed decision: a payment is K deep. Lost
// is the lovelace it paid to an address nobody holds (WrongAddress).
type Confirmed struct {
	Intent uint64 `cbor:"1,keyasint"`
	ID     TxID   `cbor:"2,keyasint"`
	Lost   uint64 `cbor:"3,keyasint,omitempty"`
}

// Adoption is one queued ImageChange of an upgrade wave.
type Adoption struct {
	At   sched.Time `cbor:"1,keyasint"`
	Pool string     `cbor:"2,keyasint"`
	Ref  string     `cbor:"3,keyasint"`
	Done bool       `cbor:"4,keyasint,omitempty"`
}
