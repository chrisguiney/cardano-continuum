package submit

import (
	"errors"
	"testing"
)

func rej(cs ...Class) Outcome {
	o := Outcome{Verdict: Rejected}
	for _, c := range cs {
		o.Failures = append(o.Failures, Failure{Class: c})
	}
	return o
}

func TestRefutes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		o    Outcome
		sent bool
		want bool
	}{
		{"first send, inputs spent", rej(BadInputs), false, true},
		{"first send, mempool failure", rej(MempoolFailure), false, true},
		{"resend, bad inputs", rej(BadInputs), true, false},
		{"resend, mempool failure", rej(MempoolFailure), true, false},
		{"resend, outside validity", rej(OutsideValidityInterval), true, false},
		{"resend, value not conserved", rej(ValueNotConserved), true, false},
		{"resend, unknown", rej(Unknown), true, false},
		{"resend, fee too small", rej(FeeTooSmall), true, true},
		{"resend, missing signer", rej(MissingVKeyWitnesses), true, true},
		{"resend, spent beside a fee error", rej(BadInputs, FeeTooSmall), true, true},
		{"resend, spent twice", rej(BadInputs, MempoolFailure), true, false},
		{"first send, accepted", Outcome{Verdict: Accepted}, false, false},
		{"first send, unreached", Outcome{Verdict: Unreached, Err: errors.New("x")}, false, false},
		{"resend, unreached", Outcome{Verdict: Unreached}, true, false},
	}
	for _, tt := range tests {
		if got := tt.o.Refutes(tt.sent); got != tt.want {
			t.Errorf("%s: Refutes(%v) = %v, want %v", tt.name, tt.sent, got, tt.want)
		}
	}
}

func TestIntrinsicReadsOnlyTheTransaction(t *testing.T) {
	t.Parallel()
	for _, c := range []Class{BadInputs, MempoolFailure, OutsideValidityInterval, ValueNotConserved, Unknown, EraMismatch, OutsideForecast, MissingScriptWitnesses, ScriptWitnessNotValid} {
		if Intrinsic(c) {
			t.Errorf("%s reads chain state but is Intrinsic", c)
		}
	}
	for _, c := range []Class{FeeTooSmall, MissingVKeyWitnesses, InvalidWitnesses, WrongNetwork, OutputTooSmall, OutputTooBig, MaxTxSize, Undecodable} {
		if !Intrinsic(c) {
			t.Errorf("%s reads only the transaction but is not Intrinsic", c)
		}
	}
}

func TestAggregateRejectsOnlyIfAllReject(t *testing.T) {
	t.Parallel()
	un := Outcome{Verdict: Unreached, Err: errors.New("timeout")}
	tests := []struct {
		name string
		outs []Outcome
		want Verdict
	}{
		{"all rejected", []Outcome{rej(FeeTooSmall), rej(BadInputs)}, Rejected},
		{"one accepted", []Outcome{rej(FeeTooSmall), {Verdict: Accepted}}, Accepted},
		{"one unreached", []Outcome{rej(FeeTooSmall), un}, Unreached},
		{"accepted beats unreached", []Outcome{un, {Verdict: Accepted}}, Accepted},
		{"no targets", nil, Unreached},
	}
	for _, tt := range tests {
		if got := Aggregate(tt.outs); got.Verdict != tt.want {
			t.Errorf("%s: %v, want %v", tt.name, got.Verdict, tt.want)
		}
	}
	got := Aggregate([]Outcome{rej(FeeTooSmall, BadInputs), rej(BadInputs)})
	if len(got.Failures) != 2 {
		t.Fatalf("failures %v, want the union without repeats", got.Failures)
	}
	if !Aggregate([]Outcome{rej(BadInputs), rej(FeeTooSmall)}).Refutes(true) {
		t.Fatal("one target's intrinsic rejection must count when every target rejected")
	}
}

func TestPerfectNamesAReportedClass(t *testing.T) {
	t.Parallel()
	fs := []Failure{{Class: BadInputs}, {Class: FeeTooSmall}}
	if c := (Perfect{}).Diagnose(nil, fs); c != BadInputs && c != FeeTooSmall {
		t.Fatalf("Perfect named %q, not a reported class", c)
	}
}
