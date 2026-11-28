package wire

import (
	"github.com/chrisguiney/cardano-continuum/internal/contract"
	"github.com/chrisguiney/cardano-continuum/internal/mistake"
	"github.com/chrisguiney/cardano-continuum/internal/protocol"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

// Deployed is the body of a Deployed decision: contract Contract
// (build.Contract) is open for locks. Address names its script.
type Deployed struct {
	Contract uint64 `json:"contract"`
	Address  string `json:"address"`
	// Validator is the registry validator the contract is an instance of.
	Validator string `json:"validator,omitempty"`
	// Script is the instance's script hash in hex.
	Script string `json:"script,omitempty"`
}

// Controlled is the body of a Controlled decision: a Control input was
// applied, or not when Why says why.
type Controlled struct {
	RequestID string `json:"request_id"`
	Op        string `json:"op"`
	Scenario  string `json:"scenario,omitempty"`
	Why       string `json:"why,omitempty"`
}

// Protocol is the body of a Protocol decision: a block or rollback changed
// what the chain view has ratified of hard forks. Version is the protocol
// version in force in epoch Epoch, the tip's; Pending is a ratified fork
// still to be enacted, nil when none is.
type Protocol struct {
	// Epoch is the tip's epoch.
	Epoch uint64 `json:"epoch"`
	// Version is the protocol version in force in Epoch.
	Version protocol.Version `json:"version"`
	// Pending is the ratified hard fork waiting for its boundary.
	Pending *Fork `json:"pending,omitempty"`
	// Plan is the hard fork the run proposes by itself while its version is
	// not yet in force; nil without a schedule or once enacted.
	Plan *protocol.Schedule `json:"plan,omitempty"`
}

// Fork is a ratified hard fork: its proposal ("txhash#index"), the version
// it moves to and the first epoch at that version.
type Fork struct {
	// Proposal is the HardForkInitiation's ID.
	Proposal string `json:"proposal"`
	// Version is the version it moves to.
	Version protocol.Version `json:"version"`
	// At is the first epoch at Version.
	At uint64 `json:"at"`
}

// ImageChange is the body of an ImageChange decision: an upgrade wave gave
// the pool actor image Ref.
type ImageChange struct {
	Ref string `json:"ref"`
}

// Guarded is the body of a Guarded decision: the stake-floor guard refused
// an intent. Target is the pool a delegation names.
type Guarded struct {
	Kind   string `json:"kind"`
	Target uint64 `json:"target,omitzero"`
	Why    string `json:"why"`
}

// PoolTier is the body of a PoolTier decision: the engine plans the pool
// actor dark (not forging) or live. Why is "agenda" or "recovery".
type PoolTier struct {
	Dark bool   `json:"dark"`
	Why  string `json:"why"`
}

// Repair is the body of a Repair decision: the pool's pod is to be
// rescheduled or given a new operational certificate.
type Repair struct{}

// Incident is the body of an Incident decision. Kind "stall": epoch Epoch
// began with the ledger's last transition at Label, so an epoch passed
// with no block and the run stops.
type Incident struct {
	Kind  string `json:"kind"`
	Epoch uint64 `json:"epoch"`
	Label uint64 `json:"label"`
}

// Spawned is the body of a Spawned decision.
type Spawned struct {
	Profile string `json:"profile"`
	// Currency is a wallet's primary currency when the run has a market.
	Currency string `json:"currency,omitempty"`
}

// Transition is the body of a Transition decision.
type Transition struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// Submitted is the body of a Submitted decision: an attempt at a payment was
// built, its inputs reserved, and it is to be submitted. A payment has one
// Submitted per attempt, all with the same Intent. Tx is the signed
// transaction.
type Submitted struct {
	Intent uint64 `json:"intent"`
	ID     TxID   `json:"id"`
	To     uint64 `json:"to"`
	Amount uint64 `json:"amount"`
	Fee    uint64 `json:"fee"`
	TTL    uint64 `json:"ttl"`
	Inputs []Ref  `json:"inputs"`
	Tx     []byte `json:"tx"`
	// Kind is the intent kind; empty for a payment. To is then the pool,
	// DRep or nobody the intent names, Amount its quantity.
	Kind string `json:"kind,omitempty"`
	// Mistakes are the honest mistakes the attempt carries.
	Mistakes mistake.KindSet `json:"mistakes,omitzero"`
	// Attack names the attack an adversary's attempt makes, or "cover"
	// for its ordinary traffic; empty for an honest actor.
	Attack string `json:"attack,omitempty"`
	// Action is a proposal's governance action type, Contract the
	// contract a lock or unlock calls.
	Action   string `json:"action,omitempty"`
	Contract uint64 `json:"contract,omitzero"`
	// Validator names the script a lock pays to or a script call runs
	// ("vesting#3", "free_mint"); empty for a transaction without one.
	Validator string `json:"validator,omitempty"`
	// Scripts are the attempt's script runs, one per redeemer, as the
	// engine's evaluation of the submitted bytes gave them.
	Scripts []contract.Run `json:"scripts,omitempty"`
	// A vote's proposal ("txhash#index"), choice (yes, no or abstain),
	// voter class (drep, spo or committee) and voting power: the lovelace
	// the engine's chain view puts behind the voter when it votes.
	Proposal string `json:"proposal,omitempty"`
	Choice   string `json:"choice,omitempty"`
	Voter    string `json:"voter,omitempty"`
	Power    uint64 `json:"power,omitzero"`
}

// Refill is the body of a Refill decision: the faucet pays To[i]
// Amounts[i] in one transaction, whose inputs are reserved like a
// payment's. A refill has no retry machine: a refused one is released and
// the next round is its retry.
type Refill struct {
	Intent  uint64   `json:"intent"`
	ID      TxID     `json:"id"`
	To      []uint64 `json:"to"`
	Amounts []uint64 `json:"amounts"`
	Fee     uint64   `json:"fee"`
	TTL     uint64   `json:"ttl"`
	Inputs  []Ref    `json:"inputs"`
	Tx      []byte   `json:"tx"`
}

// Skipped is the body of a Skipped decision: an intent the engine chose but
// did not act on.
type Skipped struct {
	Kind string `json:"kind"`
	Why  string `json:"why"`
}

// Released is the body of a Released decision: a payment has no unfinished
// attempt left and was not confirmed. Why is "expired", "conflict" or
// "gave up".
type Released struct {
	Intent uint64 `json:"intent"`
	Why    string `json:"why"`
}

// Refused is the body of a Refused decision: every node rejected an attempt
// and the answer proves it never reached a mempool, so the ledger wrote it
// off. The payment itself goes on: the machine builds the next attempt.
type Refused struct {
	Intent uint64 `json:"intent"`
	ID     TxID   `json:"id"`
	Why    string `json:"why"`
	// Detail is the node's own text for the failures Why names.
	Detail string `json:"detail,omitempty"`
}

// GaveUp is the body of a GaveUp decision: the actor's patience is spent. No
// reservation changes; the attempts stay unfinished until the ledger settles
// or writes them off.
type GaveUp struct {
	Intent uint64 `json:"intent"`
}

// Confirmed is the body of a Confirmed decision: a payment is K deep. Lost
// is the lovelace it paid to an address nobody holds (WrongAddress).
type Confirmed struct {
	Intent uint64 `json:"intent"`
	ID     TxID   `json:"id"`
	Lost   uint64 `json:"lost,omitzero"`
}

// Adoption is one queued ImageChange of an upgrade wave.
type Adoption struct {
	At   sched.Time `json:"at"`
	Pool string     `json:"pool"`
	Ref  string     `json:"ref"`
	Done bool       `json:"done,omitzero"`
}
