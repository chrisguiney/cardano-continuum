package judge

import (
	"slices"

	"github.com/chrisguiney/cardano-continuum/internal/build"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/wire"
	"github.com/chrisguiney/cardano-continuum/internal/mistake"
	"github.com/chrisguiney/cardano-continuum/internal/submit"
)

// Expect is mistake.ExpectOn the run's parameters, plus the classes a node names beside "inputs
// spent" when the attempt's own earlier copy already landed: its
// certificates are applied, so applying them again fails their rule too
// (cardano-ledger runs the CERTS rule on the inputs it still holds and
// accumulates both failures, as it does ValueNotConserved).
func Expect(pp build.Params, ks mistake.KindSet, spent bool, again []submit.Class) mistake.Expected {
	e := mistake.ExpectOn(ks, spent, pp)
	if !spent {
		return e
	}
	return WithClasses(e, again...)
}

// WithClasses adds cs to e's classes, which Allows needs sorted.
func WithClasses(e mistake.Expected, cs ...submit.Class) mistake.Expected {
	if len(cs) == 0 {
		return e
	}
	e.Classes = append(slices.Clone(e.Classes), cs...)
	slices.Sort(e.Classes)
	e.Classes = slices.Compact(e.Classes)
	return e
}

func AnswerOutcome(a wire.Answer) submit.Outcome {
	o := submit.Outcome{Verdict: submit.Unreached}
	switch a.Verdict {
	case wire.VerdictAccepted:
		o.Verdict = submit.Accepted
	case wire.VerdictRejected:
		o.Verdict, o.Failures = submit.Rejected, wire.Failures(a.Failures, a.Details)
	}
	return o
}
