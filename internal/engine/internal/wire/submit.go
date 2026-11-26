package wire

import "github.com/chrisguiney/cardano-continuum/internal/oracle"

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
	// VerdictDropped: a drop notice, not an answer to a submission: the
	// attempt Tx is in no mempool and no block the node has.
	VerdictDropped
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
	// Tx is the attempt a drop notice is about.
	Tx *TxID `cbor:"6,keyasint,omitempty"`
	// Node is the node that answered, for the oracle's findings.
	Node *oracle.Node `cbor:"7,keyasint,omitempty"`
	// Again is the node's answer to the second send of a DoubleSubmit
	// mistake. The retry machine reads only the first answer; the oracle
	// judges both.
	Again *Answer `cbor:"8,keyasint,omitempty"`
	// Rivals are the node's answers to a double-spend race's further
	// transactions, sent in order after the first.
	Rivals []Answer `cbor:"9,keyasint,omitempty"`
}

// Answer is one node answer to one send.
type Answer struct {
	Verdict  Verdict  `cbor:"1,keyasint"`
	Failures []string `cbor:"2,keyasint,omitempty"`
}

// MonitorSnapshot is the body of a MonitorSnapshot input: what the runner
// asked the nodes for the invariant monitors. The engine adds its own side
// (each view's At and Engine balances) when it handles the input.
type MonitorSnapshot struct {
	Views []oracle.ViewCheck `cbor:"1,keyasint,omitempty"`
	// Tips are the pools' chains as followed, for the fork monitor.
	Tips []oracle.Branch `cbor:"2,keyasint,omitempty"`
}

// PoolStatus is the body of a PoolStatus input: a pool actor's pod was
// observed running or not (operator status).
type PoolStatus struct {
	Pool    uint64 `cbor:"1,keyasint"`
	Running bool   `cbor:"2,keyasint"`
}
