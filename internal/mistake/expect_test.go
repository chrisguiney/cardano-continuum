package mistake

import (
	"slices"
	"testing"

	"github.com/chrisguiney/cardano-continuum/internal/submit"
)

func rejected(cs ...submit.Class) submit.Outcome {
	fs := make([]submit.Failure, len(cs))
	for i, c := range cs {
		fs[i] = submit.Failure{Class: c}
	}
	return submit.Outcome{Verdict: submit.Rejected, Failures: fs}
}

var accepted = submit.Outcome{Verdict: submit.Accepted}

func TestCatalogClasses(t *testing.T) {
	want := map[Kind]submit.Class{
		FeeTooSmall:     submit.FeeTooSmall,
		StaleInput:      submit.BadInputs,
		OutputTooSmall:  submit.OutputTooSmall,
		Expired:         submit.OutsideValidityInterval,
		CollateralShort: submit.InsufficientCollateral,
		ExUnitsLow:      submit.ValidationTagMismatch,
		MissingSigner:   submit.MissingVKeyWitnesses,
		WrongNetwork:    submit.WrongNetwork,
		WrongDatum:      submit.ValidationTagMismatch,
	}
	for _, k := range Kinds() {
		c, ok := ClassOf(k)
		w, rejects := want[k]
		if ok != rejects || c != w {
			t.Errorf("ClassOf(%v) = %q, %v; want %q, %v", k, c, ok, w, rejects)
		}
	}
	if len(Kinds()) != 11 {
		t.Fatalf("catalog has %d kinds, want 11", len(Kinds()))
	}
}

// RetrySingleExpectedVerdictBug: a node checking MissingSigner first names
// the second mistake, so both classes must be expected.
func TestExpectedIsSetPerMistake(t *testing.T) {
	e := Expect(Of(FeeTooSmall, MissingSigner), false)
	want := []submit.Class{submit.FeeTooSmall, submit.MissingVKeyWitnesses}
	slices.Sort(want)
	if !slices.Equal(e.Classes, want) || e.Accept {
		t.Fatalf("Expect = %+v, want classes %v and no acceptance", e, want)
	}
	for _, o := range []submit.Outcome{
		rejected(submit.FeeTooSmall),
		rejected(submit.MissingVKeyWitnesses),
		rejected(submit.MissingVKeyWitnesses, submit.FeeTooSmall),
	} {
		if !e.Pass(o) {
			t.Errorf("%v should pass", o.Failures)
		}
	}
	for _, o := range []submit.Outcome{
		accepted,
		rejected(),
		rejected(submit.FeeTooSmall, submit.BadInputs),
		rejected(submit.Unknown),
	} {
		if e.Pass(o) {
			t.Errorf("%+v should fail", o)
		}
	}
}

// For every order of the UTXO rules the node stops at the first failure,
// so a rejection naming any one mistake's class alone must pass.
func TestExpectedAnyRuleOrder(t *testing.T) {
	ks := Of(FeeTooSmall, StaleInput, OutputTooSmall, Expired)
	e := Expect(ks, false)
	var classes []submit.Class
	for _, k := range ks.Kinds() {
		c, _ := ClassOf(k)
		classes = append(classes, c)
	}
	for _, order := range permutations(classes) {
		if !e.Pass(rejected(order[0])) {
			t.Fatalf("order %v: first failure %s not expected", order, order[0])
		}
		if !e.Pass(rejected(order...)) {
			t.Fatalf("order %v: all failures not expected", order)
		}
	}
}

func permutations(xs []submit.Class) [][]submit.Class {
	if len(xs) <= 1 {
		return [][]submit.Class{slices.Clone(xs)}
	}
	var out [][]submit.Class
	for i := range xs {
		rest := slices.Concat(xs[:i:i], xs[i+1:])
		for _, p := range permutations(rest) {
			out = append(out, append([]submit.Class{xs[i]}, p...))
		}
	}
	return out
}

// RetryOracleIgnoresResubmitBug: once sent, "inputs spent" in either form is
// an honest answer.
func TestExpectedAfterSendIncludesSpent(t *testing.T) {
	for _, ks := range []KindSet{0, Of(DoubleSubmit), Of(FeeTooSmall), Of(WrongAddress)} {
		fresh, sent := Expect(ks, false), Expect(ks, true)
		for _, c := range []submit.Class{submit.BadInputs, submit.MempoolFailure, submit.ValueNotConserved} {
			if !sent.Pass(rejected(c)) {
				t.Errorf("%v sent: %s not expected", ks, c)
			}
		}
		if ks.Has(StaleInput) {
			continue
		}
		if fresh.Pass(rejected(submit.MempoolFailure)) {
			t.Errorf("%v fresh: MempoolFailure expected before any send", ks)
		}
		if fresh.Accept != sent.Accept {
			t.Errorf("%v: acceptance changed with sending", ks)
		}
	}
	if !Expect(0, false).Accept || !Expect(Of(WrongAddress, DoubleSubmit), false).Accept {
		t.Error("a transaction without a rejecting mistake must expect acceptance")
	}
	if Expect(Of(FeeTooSmall), true).Accept {
		t.Error("a rejecting mistake never expects acceptance")
	}
}

func TestStaleAndOwnSpendSameClass(t *testing.T) {
	stale, _ := ClassOf(StaleInput)
	if !Expect(0, true).Allows(stale) {
		t.Fatalf("a resubmit's %s is not expected", stale)
	}
	if FixFor(stale) != FixFor(submit.MempoolFailure) {
		t.Fatal("the fix for BadInputs differs from the fix for MempoolFailure")
	}
}

func TestFixForCoversCatalog(t *testing.T) {
	for _, k := range Kinds() {
		c, ok := ClassOf(k)
		if !ok {
			if Carry(Of(k), nil) != Of(k) {
				t.Errorf("%v: carried set changed with no fix", k)
			}
			continue
		}
		if !FixFor(c).Has(k) {
			t.Errorf("FixFor(%s) does not remove %v", c, k)
		}
		if Carry(Of(k, WrongAddress), []submit.Class{c}) != Of(WrongAddress) {
			t.Errorf("%v: fixing %s left %v", k, c, Carry(Of(k, WrongAddress), []submit.Class{c}))
		}
	}
	if FixFor(submit.Unknown) != 0 {
		t.Error("an unknown class fixes something")
	}
}
