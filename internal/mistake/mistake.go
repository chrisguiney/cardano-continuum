// Package mistake makes honest mistakes: it turns a correct transaction from
// internal/build into a specific wrong one, and says what a node must answer.
//
// The pipeline is Intent -> Builder (always correct) -> Inject -> Submitter
// -> Outcome -> oracle (Expected.Pass). The builder never learns about
// mistakes; Inject edits its CBOR afterwards and re-signs where the mistake
// needs a valid signature over the edited body.
//
// The catalog, with the class each mistake causes and whether Inject
// recomputes the witness set (re-signs the body):
//
//	Kind             Class                        Re-signs  Family (Utxo.tla)
//	fee_too_small    FeeTooSmallUTxO              yes       balance
//	stale_input      BadInputsUTxO                yes       inputs
//	output_too_small BabbageOutputTooSmallUTxO    yes       outputs
//	expired          OutsideValidityIntervalUTxO  yes       validity interval
//	collateral_short InsufficientCollateral       yes       collateral and phase 2
//	exunits_low      ValidationTagMismatch        yes       collateral and phase 2
//	missing_signer   MissingVKeyWitnessesUTXOW    no: drops a witness  (UTXOW)
//	wrong_network    WrongNetwork                 yes       outputs (network)
//	wrong_datum      ValidationTagMismatch        yes       collateral and phase 2
//	double_submit    none: accepted once          no: same bytes
//	wrong_address    none: accepted, funds lost   yes       outputs
//
// The rules are the "Mistakes and expected verdicts" section of
// models/specs/submit-retry/README.md: the expected verdict is a set, one
// class per mistake in any rule order, plus the "inputs spent" classes once
// the attempt (or one it conflicts with) may be at a node.
package mistake

import (
	"fmt"
	"math/bits"
	"slices"
	"strings"

	"github.com/chrisguiney/cardano-continuum/internal/submit"
)

// Kind is one honest mistake.
type Kind uint8

// Kinds of mistake.
const (
	// FeeTooSmall pays less than the minimum fee; the change keeps the rest.
	FeeTooSmall Kind = iota
	// StaleInput spends an output that is no longer unspent, as an actor
	// with an outdated view of its wallet would.
	StaleInput
	// OutputTooSmall adds an output below min-UTxO, taken from the change.
	OutputTooSmall
	// Expired sets the validity interval's upper bound to a slot the chain
	// has passed.
	Expired
	// CollateralShort declares less collateral than the fee requires,
	// returning the difference.
	CollateralShort
	// ExUnitsLow underestimates every redeemer's execution units.
	ExUnitsLow
	// MissingSigner leaves out a required key witness.
	MissingSigner
	// WrongNetwork pays an address of another network.
	WrongNetwork
	// WrongDatum supplies a redeemer the script does not accept.
	WrongDatum
	// DoubleSubmit sends the same bytes twice.
	DoubleSubmit
	// WrongAddress pays someone nobody holds the key for. The ledger
	// accepts it; the funds are lost.
	WrongAddress
	numKinds
)

// entry is one catalog row.
type entry struct {
	name  string
	class submit.Class // "" when the node accepts the mistake
	// resign reports that Inject edits the body and so re-signs it.
	resign bool
	// script reports that the mistake needs a script spend: redeemers and
	// collateral.
	script bool
}

var catalog = [numKinds]entry{
	FeeTooSmall:     {"fee_too_small", submit.FeeTooSmall, true, false},
	StaleInput:      {"stale_input", submit.BadInputs, true, false},
	OutputTooSmall:  {"output_too_small", submit.OutputTooSmall, true, false},
	Expired:         {"expired", submit.OutsideValidityInterval, true, false},
	CollateralShort: {"collateral_short", submit.InsufficientCollateral, true, true},
	ExUnitsLow:      {"exunits_low", submit.ValidationTagMismatch, true, true},
	MissingSigner:   {"missing_signer", submit.MissingVKeyWitnesses, false, false},
	WrongNetwork:    {"wrong_network", submit.WrongNetwork, true, false},
	WrongDatum:      {"wrong_datum", submit.ValidationTagMismatch, true, true},
	DoubleSubmit:    {"double_submit", "", false, false},
	WrongAddress:    {"wrong_address", "", true, false},
}

func (k Kind) String() string {
	if k < numKinds {
		return catalog[k].name
	}
	return fmt.Sprintf("Kind(%d)", k)
}

// Kinds returns every kind in catalog order.
func Kinds() []Kind {
	out := make([]Kind, numKinds)
	for i := range out {
		out[i] = Kind(i)
	}
	return out
}

// ClassOf returns the class a node reports for mistake k, and false for a
// mistake the node accepts.
func ClassOf(k Kind) (submit.Class, bool) {
	if k >= numKinds || catalog[k].class == "" {
		return "", false
	}
	return catalog[k].class, true
}

// Resigns reports whether Inject recomputes the witness set for k: the
// mistake edits the body, so every key witness is made again over the new
// body hash. MissingSigner drops one witness instead, and DoubleSubmit
// changes nothing.
func Resigns(k Kind) bool { return k < numKinds && catalog[k].resign }

// Script reports whether k needs a transaction that spends or mints with a
// Plutus script.
func Script(k Kind) bool { return k < numKinds && catalog[k].script }

// KindSet is a set of kinds. It is persisted with a payment.
type KindSet uint16

// Of returns the set of ks.
func Of(ks ...Kind) KindSet {
	var s KindSet
	for _, k := range ks {
		if k < numKinds {
			s |= 1 << k
		}
	}
	return s
}

// Has reports whether k is in s.
func (s KindSet) Has(k Kind) bool { return k < numKinds && s&(1<<k) != 0 }

// Without returns s less every kind in o.
func (s KindSet) Without(o KindSet) KindSet { return s &^ o }

// Kinds returns the members of s in catalog order. Bits past the catalog
// are ignored.
func (s KindSet) Kinds() []Kind {
	s &= 1<<numKinds - 1
	out := make([]Kind, 0, bits.OnesCount16(uint16(s)))
	for _, k := range Kinds() {
		if s.Has(k) {
			out = append(out, k)
		}
	}
	return out
}

func (s KindSet) String() string {
	ks := s.Kinds()
	names := make([]string, len(ks))
	for i, k := range ks {
		names[i] = k.String()
	}
	return "{" + strings.Join(names, ",") + "}"
}

// spentClasses are the answers a node gives when some of a transaction's
// inputs are gone: the mempool's refusal before the UTXO rules run when all
// of them are, otherwise the UTXO rule's BadInputs together with
// ValueNotConserved, since the ledger sums only the inputs it still holds
// (cardano-ledger accumulates both failures). The contract names BadInputs
// and MempoolFailure; ValueNotConserved is added on the same ground, and is
// among rule 2's classes that prove nothing after a send.
var spentClasses = []submit.Class{submit.BadInputs, submit.MempoolFailure, submit.ValueNotConserved}

// FixFor returns the mistakes a fix for class c removes: the builder is
// always correct, so fixing a class means no longer injecting the mistakes
// that cause it. Every "inputs spent" class is fixed by refreshing the
// wallet's view, which removes StaleInput. A class no mistake causes fixes
// nothing.
func FixFor(c submit.Class) KindSet {
	var s KindSet
	for _, k := range Kinds() {
		if catalog[k].class == c {
			s |= Of(k)
		}
	}
	if slices.Contains(spentClasses, c) {
		s |= Of(StaleInput)
	}
	return s
}

// Carry is what the next attempt carries: the injected mistakes less every
// one a fix so far removes. fixed is submit.Machine.Fixed.
func Carry(injected KindSet, fixed []submit.Class) KindSet {
	for _, c := range fixed {
		injected = injected.Without(FixFor(c))
	}
	return injected
}

// Expected is the oracle's verdict for one send of a transaction.
type Expected struct {
	// Classes are every class an honest node may name, sorted. A rejection
	// passes when it names at least one and only these.
	Classes []submit.Class
	// Accept reports that the node may accept the transaction.
	Accept bool
	// Lost is the lovelace paid to an address nobody holds, which the
	// audit records when the transaction lands.
	Lost uint64
}

// Expect returns the verdict for a transaction carrying the mistakes ks.
// spent is true once this attempt, or an attempt it conflicts with, may be
// at a node (a send returned Accepted or Unreached, or a restart left it
// unfinished), or the harness's adversary spent one of its inputs. With no
// rejecting mistake the node may accept; after spent it may also say the
// inputs are gone.
func Expect(ks KindSet, spent bool) Expected {
	var e Expected
	for _, k := range ks.Kinds() {
		if c, ok := ClassOf(k); ok {
			e.Classes = append(e.Classes, c)
		}
	}
	e.Accept = len(e.Classes) == 0
	if spent {
		e.Classes = append(e.Classes, spentClasses...)
	}
	slices.Sort(e.Classes)
	e.Classes = slices.Compact(e.Classes)
	return e
}

// Allows reports whether c is an expected class.
func (e Expected) Allows(c submit.Class) bool {
	_, ok := slices.BinarySearch(e.Classes, c)
	return ok
}

// Pass judges one node answer. An acceptance passes when Accept is set; a
// rejection when it names at least one class and every one is expected, in
// whatever order the node's rules ran. Unknown is never expected. No answer
// (Unreached) says nothing about the transaction and passes.
func (e Expected) Pass(o submit.Outcome) bool {
	switch o.Verdict {
	case submit.Accepted:
		return e.Accept
	case submit.Rejected:
		if len(o.Failures) == 0 {
			return false
		}
		for _, f := range o.Failures {
			if !e.Allows(f.Class) {
				return false
			}
		}
		return true
	}
	return true
}

func (e Expected) String() string {
	parts := make([]string, 0, len(e.Classes)+1)
	if e.Accept {
		parts = append(parts, "accepted")
	}
	for _, c := range e.Classes {
		parts = append(parts, string(c))
	}
	s := "{" + strings.Join(parts, "|") + "}"
	if e.Lost > 0 {
		s += fmt.Sprintf(" lost=%d", e.Lost)
	}
	return s
}
