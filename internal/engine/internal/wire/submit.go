package wire

import (
	"errors"

	"github.com/chrisguiney/cardano-continuum/internal/oracle"
	"github.com/chrisguiney/cardano-continuum/internal/submit"
)

// ErrUnknownVerdict: a SubmitResult whose verdict is missing or unknown.
var ErrUnknownVerdict = errors.New("engine: submit result has no known verdict")

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

// Submission is a payment waiting for the node's answer. Twice asks for the
// bytes to be sent a second time (a DoubleSubmit mistake). It is never
// logged.
type Submission struct {
	Intent uint64
	Actor  uint64
	ID     TxID
	Tx     []byte
	Twice  bool
	// Rivals are sent right after Tx: a double-spend race.
	Rivals []Rival
}

// SubmitResult is the body of a SubmitResult input: what the node said to a
// submission.
type SubmitResult struct {
	Intent  uint64  `json:"intent"`
	Verdict Verdict `json:"verdict"`
	Reason  string  `json:"reason,omitempty"`
	// Resend marks a rejection of a transaction that may have been sent
	// before, by a process that died before logging the answer. The
	// transaction's own arrival can cause that rejection, so it proves
	// nothing. It is recorded for rejections only, the one verdict it
	// changes, so a resumed accepted submission logs the same record as an
	// uninterrupted one.
	Resend bool `json:"resend,omitzero"`
	// Failures are the classes the node named, for a rejection. The retry
	// machine reads them: after an earlier send only a class that depends on
	// the transaction alone proves the attempt never reached a mempool.
	Failures []string `json:"failures,omitempty"`
	// Details are the node's own text for each of Failures, in order: the
	// class alone does not say why, a ConwayMempoolFailure least of all.
	Details []string `json:"details,omitempty"`
	// Tx is the attempt a drop notice is about.
	Tx *TxID `json:"tx,omitzero"`
	// Node is the node that answered, for the oracle's findings.
	Node *oracle.Node `json:"node,omitzero"`
	// Again is the node's answer to the second send of a DoubleSubmit
	// mistake. The retry machine reads only the first answer; the oracle
	// judges both.
	Again *Answer `json:"again,omitzero"`
	// Rivals are the node's answers to a double-spend race's further
	// transactions, sent in order after the first.
	Rivals []Answer `json:"rivals,omitempty"`
}

// Answer is one node answer to one send.
type Answer struct {
	Verdict  Verdict  `json:"verdict"`
	Failures []string `json:"failures,omitempty"`
	// Details are the node's text for each of Failures, as in
	// SubmitResult.
	Details []string `json:"details,omitempty"`
}

// Failures rebuilds the failures a logged answer names: classes with the
// node's text when the answer kept it.
func Failures(classes, details []string) []submit.Failure {
	out := make([]submit.Failure, 0, len(classes))
	for i, c := range classes {
		f := submit.Failure{Class: submit.Class(c)}
		if i < len(details) {
			f.Detail = details[i]
		}
		out = append(out, f)
	}
	return out
}

// MonitorSnapshot is the body of a MonitorSnapshot input: what the runner
// asked the nodes for the invariant monitors. The engine adds its own side
// (each view's At and Engine balances) when it handles the input.
type MonitorSnapshot struct {
	Views []oracle.ViewCheck `json:"views,omitempty"`
	// Tips are the pools' chains as followed, for the fork monitor.
	Tips []oracle.Branch `json:"tips,omitempty"`
}

// PoolStatus is the body of a PoolStatus input: a pool actor's pod was
// observed running or not (operator status).
type PoolStatus struct {
	Pool    uint64 `json:"pool"`
	Running bool   `json:"running"`
}

// PoolPlan is what the engine wants of one pool actor's ContinuumPool.
type PoolPlan struct {
	Actor uint64
	// Genesis is the genesis pool index, or -1 for a pool the engine
	// registered.
	Genesis int
	Dark    bool
	// Image is the image an upgrade wave gave the pool; empty is the
	// network's default.
	Image string
}
