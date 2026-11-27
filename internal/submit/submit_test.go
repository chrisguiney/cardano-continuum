package submit

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/chrisguiney/cardano-continuum/internal/fakechain"
	"github.com/chrisguiney/cardano-continuum/internal/keys"
)

func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

func TestFakeAccepts(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	o := Send(context.Background(), NewFake(f.chain), fakechain.EraConway, f.encode(f.good()))
	if o.Verdict != Accepted || o.Err != nil || o.Target != "fakechain" {
		t.Fatalf("outcome = %+v, want accepted by fakechain", o)
	}
	if o.Dead() {
		t.Fatal("an accepted submit is not dead")
	}
}

// TestRejectionClasses submits one broken transaction per class and requires
// the outcome to carry exactly the class the fake chain produced, by name.
func TestRejectionClasses(t *testing.T) {
	t.Parallel()
	wrongNet := uint8(1)
	tests := []struct {
		name string
		want Class
		mod  func(f *fixture, s *spec)
	}{
		{"unknown input", BadInputs, func(_ *fixture, s *spec) { s.extraIn = true }},
		{"fee too small", FeeTooSmall, func(_ *fixture, s *spec) { s.fee = 1 }},
		{"value not conserved", ValueNotConserved, func(_ *fixture, s *spec) { s.change = 1 }},
		{"ttl passed", OutsideValidityInterval, func(f *fixture, s *spec) {
			if err := f.chain.AdvanceTo(10); err != nil {
				t.Fatal(err)
			}
			s.ttl = 11
		}},
		{"output too small", OutputTooSmall, func(_ *fixture, s *spec) { s.pay = 100_000 }},
		{"missing witness", MissingVKeyWitnesses, func(f *fixture, s *spec) { s.signers = []keys.Wallet{f.bob} }},
		{"wrong network", WrongNetwork, func(_ *fixture, s *spec) {
			store, err := keys.New([32]byte{1}, 1)
			if err != nil {
				t.Fatal(err)
			}
			s.dest = store.Wallet(2).Address()
		}},
		{"network in body", WrongNetworkInTxBody, func(_ *fixture, s *spec) { s.network = &wrongNet }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			s := f.good()
			tt.mod(f, &s)
			o := Send(context.Background(), NewFake(f.chain), fakechain.EraConway, f.encode(s))
			if o.Verdict != Rejected {
				t.Fatalf("verdict = %v (%v), want rejected", o.Verdict, o.Err)
			}
			if !o.Has(tt.want) {
				t.Fatalf("failures %v lack %s", o.Failures, tt.want)
			}
			if !o.Dead() {
				t.Fatal("a node refusal is dead: the tx never reached a mempool")
			}
		})
	}
}

// TestClassNamesMatchFakechain pins the shared vocabulary so a verdict from
// the fake and one decoded from a real node compare directly.
func TestClassNamesMatchFakechain(t *testing.T) {
	t.Parallel()
	pairs := map[Class]fakechain.Class{
		BadInputs: fakechain.BadInputs, FeeTooSmall: fakechain.FeeTooSmall,
		ValueNotConserved: fakechain.ValueNotConserved, OutsideValidityInterval: fakechain.OutsideValidityInterval,
		OutputTooSmall: fakechain.OutputTooSmall, OutputTooBig: fakechain.OutputTooBig,
		OutputBootAddrAttrs: fakechain.OutputBootAddrAttrs, MaxTxSize: fakechain.MaxTxSize,
		InputSetEmpty: fakechain.InputSetEmpty, WrongNetwork: fakechain.WrongNetwork,
		WrongNetworkWithdrawal: fakechain.WrongNetworkWithdrawal, WrongNetworkInTxBody: fakechain.WrongNetworkInTxBody,
		OutsideForecast: fakechain.OutsideForecast, MissingVKeyWitnesses: fakechain.MissingVKeyWitnesses,
		InvalidWitnesses: fakechain.InvalidWitnesses, MissingScriptWitnesses: fakechain.MissingScriptWitnesses,
		ScriptWitnessNotValid: fakechain.ScriptWitnessNotValid, NonDisjointRefInputs: fakechain.NonDisjointRefInputs,
	}
	for ours, theirs := range pairs {
		if string(ours) != string(theirs) {
			t.Errorf("%q != fakechain %q", ours, theirs)
		}
	}
}

func TestUnmappedFakeClassKeptVerbatim(t *testing.T) {
	t.Parallel()
	o := outcomeOf("fakechain", &fakechain.RejectError{Failures: []fakechain.Failure{
		{Class: "gouroboros:SomeRule", Err: errors.New("boom")},
	}})
	if o.Verdict != Rejected || len(o.Failures) != 1 ||
		o.Failures[0].Class != "gouroboros:SomeRule" || o.Failures[0].Detail != "boom" {
		t.Fatalf("outcome = %+v", o)
	}
}

type failTarget struct{ err error }

func (failTarget) Name() string { return "flaky" }
func (t failTarget) Submit(context.Context, uint, []byte) error {
	return t.err
}

func TestTransportErrorIsNotRefusal(t *testing.T) {
	t.Parallel()
	for _, err := range []error{context.DeadlineExceeded, errors.New("connection reset"), context.Canceled} {
		o := Send(context.Background(), failTarget{err}, 6, nil)
		if o.Verdict != Unreached {
			t.Fatalf("%v: verdict = %v, want unreached", err, o.Verdict)
		}
		if o.Dead() {
			t.Fatalf("%v: a transport error must not be dead: the tx may have arrived", err)
		}
		if !errors.Is(o.Err, err) || len(o.Failures) != 0 {
			t.Fatalf("outcome = %+v", o)
		}
	}
}

func TestUndecodableIsRejection(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	o := Send(context.Background(), NewFake(f.chain), fakechain.EraConway, []byte{0xff})
	if o.Verdict != Rejected || !o.Has(Undecodable) {
		t.Fatalf("outcome = %+v, want rejected as undecodable", o)
	}
}

type slowTarget struct{ name string }

func (s slowTarget) Name() string { return s.name }
func (s slowTarget) Submit(ctx context.Context, _ uint, _ []byte) error {
	<-ctx.Done()
	return ctx.Err()
}

func TestFanoutPerTargetOutcomes(t *testing.T) {
	t.Parallel()
	good, bad := newFixture(t), newFixture(t)
	if err := bad.chain.AdvanceTo(10); err != nil { // the same tx has a ttl in the past here
		t.Fatal(err)
	}
	s := good.good()
	s.ttl = 11
	tx := good.encode(s)
	targets := []Target{NewFake(good.chain), NewFake(bad.chain), failTarget{errors.New("down")}}
	outs := Fanout(context.Background(), targets, fakechain.EraConway, tx)
	if len(outs) != len(targets) {
		t.Fatalf("outcomes = %d, want %d", len(outs), len(targets))
	}
	want := []Verdict{Accepted, Rejected, Unreached}
	for i, o := range outs {
		if o.Verdict != want[i] || o.Target != targets[i].Name() {
			t.Errorf("outcome %d = %+v, want %v from %s", i, o, want[i], targets[i].Name())
		}
	}
	if !outs[1].Has(OutsideValidityInterval) {
		t.Errorf("second outcome failures = %v", outs[1].Failures)
	}
}

func TestFanoutCancelUnblocksAndJoins(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	outs := Fanout(ctx, []Target{slowTarget{"a"}, slowTarget{"b"}}, 6, nil)
	for _, o := range outs {
		if o.Verdict != Unreached || !errors.Is(o.Err, context.DeadlineExceeded) {
			t.Fatalf("outcome = %+v", o)
		}
	}
}
