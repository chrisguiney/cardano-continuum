// Package oracle decides whether what happened is what should have happened,
// and files a Finding when it is not.
//
// Two kinds of check run here, both pure. The verdict oracle (Judge,
// Differential) compares each node's answer to a submitted attempt with the
// verdict set internal/mistake expects, following the "Mistakes and expected
// verdicts" contract of models/specs/submit-retry/README.md: a rejection
// passes when it names at least one class and every class it names is
// expected, in any rule order; an unmapped (Unknown) class is reported, never
// passed. On a network mixing implementations, two implementations answering
// the same transaction differently on a rule that reads no chain state is a
// finding of its own, since such a network can fork on it.
//
// The invariant monitors (Monitors) each check one invariant of the TLA+
// contracts over a Snapshot the engine assembles: value conservation, no
// intent paid twice and no reservation leaked or doubly held
// (models/specs/planner-reservations), the settled point never moving back
// (models/specs/engine-recovery, design A), the engine's view matching the
// node's, no epoch without a block and the stake floor
// (models/specs/stake-floor), and pool tips agreeing within k.
//
// A finding's ID hashes its kind and its identifying evidence only, so a
// replay, a resend or a later snapshot showing the same fault gives the same
// ID and Set keeps one. Findings are decisions derived from inputs: the
// engine logs each new one as a KindFinding decision record, and replay
// regenerates it.
//
//	```mermaid
//	flowchart LR
//	  sub[submitter answers] -- Attempt --> judge[Judge / Differential]
//	  eng[engine state, Querier, operator status] -- Snapshot --> mon[Monitors]
//	  judge --> set[Set: dedup by ID]
//	  mon --> set
//	  set -- new findings --> log[(event log: Finding decisions)]
//	  log --> sink[sink: finding Parquet table]
//	  set -- Findings --> api[httpapi /api/findings]
//	```
package oracle

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"

	"github.com/chrisguiney/cardano-continuum/internal/submit"
)

// Severity ranks a finding.
type Severity uint8

// Severities, least severe first.
const (
	// Warn: suspicious but not a broken contract on its own, such as a node
	// naming a class the transaction does not violate beside one it does.
	Warn Severity = iota + 1
	// Error: a broken contract the run survives.
	Error
	// Fatal: the run's results can no longer be trusted, or the chain
	// cannot recover: value lost, an intent paid twice, a deep fork, a
	// frozen ledger.
	Fatal
)

func (s Severity) String() string {
	switch s {
	case Warn:
		return "warn"
	case Error:
		return "error"
	case Fatal:
		return "fatal"
	}
	return fmt.Sprintf("severity(%d)", uint8(s))
}

// Kind names what a finding found.
type Kind string

// Verdict-oracle kinds.
const (
	// UnexpectedAccept: a node accepted a transaction it had to reject.
	UnexpectedAccept Kind = "unexpected_accept"
	// UnexpectedReject: a node rejected a transaction it may accept, naming
	// a class nothing explains.
	UnexpectedReject Kind = "unexpected_reject"
	// WrongClass: a node rejected a transaction that had to be rejected,
	// but named a class outside the expected set.
	WrongClass Kind = "wrong_class"
	// Unclassified: a rejection with no class, or one the decoder could not
	// map (submit.Unknown). It is never passed: the expected class may be
	// hiding behind the unmapped one.
	Unclassified Kind = "unclassified_rejection"
	// VerdictSplit: two implementations disagree on accepting the same
	// transaction for a reason no timing explains.
	VerdictSplit Kind = "verdict_split"
)

// Monitor kinds.
const (
	ValueNotConserved Kind = "value_not_conserved"
	PaidTwice         Kind = "intent_paid_twice"
	ReservationLeaked Kind = "reservation_leaked"
	ReservationDouble Kind = "reservation_doubly_held"
	Unreserved        Kind = "input_unreserved"
	SettledBack       Kind = "settled_point_moved_back"
	ViewMismatch      Kind = "view_mismatch"
	Frozen            Kind = "chain_frozen"
	DeepFork          Kind = "fork_deeper_than_k"
	FloorBreached     Kind = "stake_floor_breached"
	// RogueAdopted: an honest node selected a block the rogue pool built
	// to break a consensus rule.
	RogueAdopted Kind = "invalid_block_adopted"
)

// Node is one node involved in a finding: its implementation, the pool it
// runs, and the build provenance from its ContinuumPool status.
type Node struct {
	Impl   string `cbor:"1,keyasint"`
	Pool   string `cbor:"2,keyasint,omitempty"`
	Digest string `cbor:"3,keyasint,omitempty"`
	// Dirty is BuildInfo.Dirty: the image included uncommitted source, so
	// its commit does not reproduce it.
	Dirty bool `cbor:"4,keyasint,omitempty"`
	// Revisions is BuildInfo.Revisions: the commit of each dependency group
	// (dingo, gouroboros, ...).
	Revisions map[string]string `cbor:"5,keyasint,omitempty"`
}

// Fact is one piece of evidence. Key facts identify the finding and enter
// its ID; the rest describe it.
type Fact struct {
	Name  string `cbor:"1,keyasint"`
	Value string `cbor:"2,keyasint"`
	Key   bool   `cbor:"3,keyasint,omitempty"`
}

// Verdict is one node's answer as a finding records it.
type Verdict struct {
	Node Node `cbor:"1,keyasint"`
	// Verdict is "accepted", "rejected" or "unreached".
	Verdict string         `cbor:"2,keyasint"`
	Classes []submit.Class `cbor:"3,keyasint,omitempty"`
	// Detail is the node's own text for the classes the decoder could not
	// map.
	Detail string `cbor:"4,keyasint,omitempty"`
}

// Expectation is the verdict set the oracle expected (mistake.Expected).
type Expectation struct {
	Classes []submit.Class `cbor:"1,keyasint,omitempty"`
	Accept  bool           `cbor:"2,keyasint,omitempty"`
}

func (e Expectation) String() string {
	cs := classList(e.Classes)
	switch {
	case e.Accept && cs == "":
		return "accept"
	case e.Accept:
		return "accept, or reject naming only " + cs
	}
	return "reject naming only " + cs
}

// Finding is one oracle or monitor disagreement. It is also the body of a
// KindFinding record.
type Finding struct {
	ID       string   `cbor:"1,keyasint"`
	Kind     Kind     `cbor:"2,keyasint"`
	Severity Severity `cbor:"3,keyasint"`
	// Seq is the log seq of the input the finding was derived from: the
	// submit result, or the snapshot's input.
	Seq     uint64 `cbor:"4,keyasint"`
	Summary string `cbor:"5,keyasint"`
	// Tx is the transaction's CBOR, for verdict findings.
	Tx       []byte       `cbor:"6,keyasint,omitempty"`
	Expected *Expectation `cbor:"7,keyasint,omitempty"`
	// Verdicts are every node answer to the transaction.
	Verdicts []Verdict `cbor:"8,keyasint,omitempty"`
	Facts    []Fact    `cbor:"9,keyasint,omitempty"`
	// Nodes are the nodes the finding blames or involves.
	Nodes []Node `cbor:"10,keyasint,omitempty"`
	Seed  uint64 `cbor:"11,keyasint"`
	// Replay is the command line that replays the run's log.
	Replay string `cbor:"12,keyasint,omitempty"`
	// Reproducible is false when any involved node ran a dirty build.
	Reproducible bool `cbor:"13,keyasint,omitempty"`
	// TxHash is the transaction's ID, for verdict findings.
	TxHash []byte `cbor:"14,keyasint,omitempty"`
	// Block is the block's CBOR, for a RogueAdopted finding.
	Block []byte `cbor:"15,keyasint,omitempty"`
}

// Run identifies the run findings come from, for their replay command.
type Run struct {
	Seed uint64
	// Log is the event log's path; without it a finding has no replay
	// command.
	Log string
	// Flags are the population and chain flags the run was started with:
	// replay refuses a log recorded under other ones.
	Flags []string
}

// Command is the replay command line, or "" without a log.
func (r Run) Command() string {
	if r.Log == "" {
		return ""
	}
	args := append([]string{"continuum", "replay", "--seed", uitoa(r.Seed)}, r.Flags...)
	args = append(args, r.Log)
	for i, a := range args {
		args[i] = quote(a)
	}
	return strings.Join(args, " ")
}

// quote single-quotes an argument a POSIX shell would split or expand.
func quote(a string) string {
	plain := func(r rune) bool {
		return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_./=:,+@", r)
	}
	if a != "" && strings.IndexFunc(a, func(r rune) bool { return !plain(r) }) < 0 {
		return a
	}
	return "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
}

// seal completes a finding: its ID from the kind and key facts, the run's
// provenance, and whether its nodes' builds reproduce it.
func seal(f Finding, r Run, seq uint64) Finding {
	f.Seq, f.Seed, f.Replay = seq, r.Seed, r.Command()
	f.Reproducible = !slices.ContainsFunc(f.Nodes, func(n Node) bool { return n.Dirty })
	f.ID = findingID(f.Kind, f.Facts)
	return f
}

// findingID is 128 bits of SHA-256 over the kind and the key facts, each
// field NUL-terminated so no two fact lists give the same input.
func findingID(k Kind, facts []Fact) string {
	h := sha256.New()
	h.Write([]byte(k))
	h.Write([]byte{0})
	for _, f := range facts {
		if !f.Key {
			continue
		}
		h.Write([]byte(f.Name))
		h.Write([]byte{0})
		h.Write([]byte(f.Value))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)[:16])
}

func key(name, value string) Fact  { return Fact{Name: name, Value: value, Key: true} }
func note(name, value string) Fact { return Fact{Name: name, Value: value} }

func classList(cs []submit.Class) string {
	s := make([]string, len(cs))
	for i, c := range cs {
		s[i] = string(c)
	}
	return strings.Join(s, ",")
}
