// Package submit delivers transactions to nodes and reports what each node
// said. It reports facts only; it never releases a reservation.
//
// An outcome is one of three verdicts. Accepted: a node took the
// transaction into its mempool, which is not confirmation. Rejected: a node
// refused it, so it provably never reached a mempool and the attempt is
// dead. Unreached: a dial, timeout, closed connection or cancelled context,
// after which the transaction may or may not have arrived. Only a rejection
// is dead; releasing on an unreached outcome is the hole
// models/specs/planner-reservations calls PlannerReleaseOnSubmitErrorBug.
package submit

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
)

// Target is one node a transaction can be submitted to. era is the
// hard-fork-combinator era index of the transaction.
type Target interface {
	Submit(ctx context.Context, era uint, tx []byte) error
	Name() string
}

// Class names a ledger predicate failure as cardano-ledger names the
// constructor, so a verdict from fakechain and one decoded from a real node
// compare directly.
type Class string

// Failure classes. The first block matches internal/fakechain's names.
const (
	BadInputs               Class = "BadInputsUTxO"
	FeeTooSmall             Class = "FeeTooSmallUTxO"
	ValueNotConserved       Class = "ValueNotConservedUTxO"
	OutsideValidityInterval Class = "OutsideValidityIntervalUTxO"
	OutputTooSmall          Class = "BabbageOutputTooSmallUTxO"
	OutputTooBig            Class = "OutputTooBigUTxO"
	OutputBootAddrAttrs     Class = "OutputBootAddrAttrsTooBig"
	MaxTxSize               Class = "MaxTxSizeUTxO"
	InputSetEmpty           Class = "InputSetEmptyUTxO"
	WrongNetwork            Class = "WrongNetwork"
	WrongNetworkWithdrawal  Class = "WrongNetworkWithdrawal"
	WrongNetworkInTxBody    Class = "WrongNetworkInTxBody"
	OutsideForecast         Class = "OutsideForecast"
	MissingVKeyWitnesses    Class = "MissingVKeyWitnessesUTXOW"
	InvalidWitnesses        Class = "InvalidWitnessesUTXOW"
	MissingScriptWitnesses  Class = "MissingScriptWitnessesUTXOW"
	ScriptWitnessNotValid   Class = "ScriptWitnessNotValidatingUTXOW"
	NonDisjointRefInputs    Class = "BabbageNonDisjointRefInputs"

	// EraMismatch: the transaction's era is not the ledger's era.
	EraMismatch Class = "HardForkApplyTxErrWrongEra"
	// MempoolFailure: the node's mempool refused it before the UTXO rules,
	// with free text; "all inputs are spent" is the common one.
	MempoolFailure Class = "ConwayMempoolFailure"
	// Undecodable: the bytes are not a transaction; a node fails these in
	// its deserialiser, before any ledger rule.
	Undecodable Class = "DeserialiseFailure"
	// Unknown: a rejection no mapping covers. The Detail keeps its text.
	Unknown Class = "Unknown"
)

// Failure is one predicate a node reported.
type Failure struct {
	Class Class
	// Detail is the node's own text for the failure.
	Detail string
}

// RejectedError is a node's refusal: it never reached the mempool.
type RejectedError struct{ Failures []Failure }

func (e *RejectedError) Error() string {
	cs := make([]string, len(e.Failures))
	for i, f := range e.Failures {
		cs[i] = string(f.Class)
	}
	return "transaction rejected: " + strings.Join(cs, ", ")
}

// Verdict is what a submit established about the transaction.
type Verdict int

// Verdicts.
const (
	// Unreached: the node's answer is unknown; the transaction may be in a mempool.
	Unreached Verdict = iota
	Accepted
	Rejected
)

func (v Verdict) String() string {
	switch v {
	case Accepted:
		return "accepted"
	case Rejected:
		return "rejected"
	default:
		return "unreached"
	}
}

// Outcome is one target's answer to one submit.
type Outcome struct {
	Target  string
	Verdict Verdict
	// Failures is set for Rejected only.
	Failures []Failure
	// Err is set for Unreached only.
	Err error
}

// Dead reports whether the attempt provably never reached a node. A
// transport error is not dead.
func (o Outcome) Dead() bool { return o.Verdict == Rejected }

// Has reports whether c is among the failures.
func (o Outcome) Has(c Class) bool {
	for _, f := range o.Failures {
		if f.Class == c {
			return true
		}
	}
	return false
}

// Send submits tx to t and reports the outcome.
func Send(ctx context.Context, t Target, era uint, tx []byte) Outcome {
	return outcomeOf(t.Name(), t.Submit(ctx, era, tx))
}

// Fanout submits tx to every target concurrently and returns one outcome
// per target, in target order. It returns after every submit has returned;
// cancel ctx to stop a hung one.
func Fanout(ctx context.Context, targets []Target, era uint, tx []byte) []Outcome {
	out := make([]Outcome, len(targets))
	var wg sync.WaitGroup
	for i, t := range targets {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out[i] = Send(ctx, t, era, tx)
		}()
	}
	wg.Wait()
	return out
}

func outcomeOf(name string, err error) Outcome {
	o := Outcome{Target: name}
	if err == nil {
		o.Verdict = Accepted
		return o
	}
	if fs, ok := rejection(err); ok {
		o.Verdict, o.Failures = Rejected, fs
		return o
	}
	o.Verdict, o.Err = Unreached, fmt.Errorf("submit to %s: %w", name, err)
	return o
}

// rejection extracts the failures when err is a node's refusal.
func rejection(err error) ([]Failure, bool) {
	var re *RejectedError
	if errors.As(err, &re) {
		return re.Failures, true
	}
	if fs, ok := fakeRejection(err); ok {
		return fs, true
	}
	return ledgerRejection(err)
}
