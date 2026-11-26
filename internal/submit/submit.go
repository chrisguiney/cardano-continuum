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
	"slices"
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
	InsufficientCollateral  Class = "InsufficientCollateral"
	// ExUnitsTooBig: the redeemers declare more than maxTxExUnits.
	ExUnitsTooBig Class = "ExUnitsTooBigUTxO"
	// ValidationTagMismatch: a script failed while the transaction declared
	// its scripts pass (or the reverse); a node refuses it from its mempool.
	ValidationTagMismatch Class = "ValidationTagMismatch"

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

// Dead reports whether a rejection of a transaction's first send proves it
// never reached a node. It is sound only for a first send: after an earlier
// send that was accepted or unanswered, "inputs spent" can be the
// transaction's own doing. Use Refutes for anything that may be a resend. A
// transport error is not dead.
func (o Outcome) Dead() bool { return o.Verdict == Rejected }

// Refutes reports whether this answer proves the attempt never reached a
// mempool, given whether an earlier send of the same bytes returned Accepted
// or Unreached (or the process restarted with the attempt unfinished). A
// first send rejected for any reason is refuted. After an earlier send only
// a rejection naming an Intrinsic class is: BadInputs, MempoolFailure and
// the other state-dependent rules are what a node answers to bytes it
// already holds or has already seen in a block. The actor's diagnosis never
// enters. See models/specs/submit-retry, rule 2.
func (o Outcome) Refutes(sentBefore bool) bool {
	if o.Verdict != Rejected {
		return false
	}
	if !sentBefore {
		return true
	}
	for _, f := range o.Failures {
		if Intrinsic(f.Class) {
			return true
		}
	}
	return false
}

// Intrinsic reports whether a rule's verdict reads only the transaction and
// the protocol parameters, so a resend cannot change it and the same
// rejection after an earlier send still proves the bytes never landed.
// Rules that read the UTXO set, the slot or the era are not: the
// transaction's own earlier arrival can trigger them. A witness check reads
// the transaction and the inputs' addresses; reference scripts and phase-2
// results read chain state, so they are left out.
func Intrinsic(c Class) bool {
	switch c {
	case FeeTooSmall, MissingVKeyWitnesses, InvalidWitnesses,
		WrongNetwork, WrongNetworkWithdrawal, WrongNetworkInTxBody,
		OutputTooSmall, OutputTooBig, OutputBootAddrAttrs, MaxTxSize,
		InputSetEmpty, NonDisjointRefInputs, Undecodable:
		return true
	}
	return false
}

// Aggregate is the answer to one send across every target. An attempt is
// rejected only when every target rejected it, with the union of their
// failures; one Accepted or Unreached answer means it may be at a node.
// Accepted wins over Unreached. No answers at all is Unreached.
func Aggregate(outs []Outcome) Outcome {
	agg := Outcome{Target: "all", Verdict: Rejected}
	if len(outs) == 0 {
		return Outcome{Target: "all", Verdict: Unreached, Err: errors.New("submit: no targets")}
	}
	for _, o := range outs {
		switch o.Verdict {
		case Accepted:
			return Outcome{Target: "all", Verdict: Accepted}
		case Unreached:
			if agg.Verdict != Unreached {
				agg = Outcome{Target: "all", Verdict: Unreached, Err: o.Err}
			}
		default:
			if agg.Verdict == Rejected {
				agg.Failures = unionFailures(agg.Failures, o.Failures)
			}
		}
	}
	return agg
}

func unionFailures(have, more []Failure) []Failure {
	for _, f := range more {
		if !slices.ContainsFunc(have, func(h Failure) bool { return h.Class == f.Class }) {
			have = append(have, f)
		}
	}
	return have
}

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
