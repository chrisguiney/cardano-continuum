package submit

import (
	"errors"
	"strings"
	"testing"

	"github.com/blinklabs-io/gouroboros/cbor"
	"github.com/blinklabs-io/gouroboros/ledger"
	"github.com/blinklabs-io/gouroboros/protocol/localtxsubmission"
)

func conway(inner error) error {
	return localtxsubmission.TransactionRejectedError{Reason: &ledger.ShelleyTxValidationError{
		Era: 6,
		Err: ledger.ApplyTxError{Failures: []error{&ledger.UtxowFailure{Err: &ledger.UtxoFailure{Era: 6, Err: inner}}}},
	}}
}

func TestClassifyLedgerErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want Class
	}{
		{"bad inputs", conway(&ledger.BadInputsUtxo{}), BadInputs},
		{"fee", conway(&ledger.FeeTooSmallUtxo{}), FeeTooSmall},
		{"value", conway(&ledger.ValueNotConservedUtxo{}), ValueNotConserved},
		{"validity", conway(&ledger.OutsideValidityIntervalUtxo{}), OutsideValidityInterval},
		{"babbage output", conway(&ledger.BabbageOutputTooSmallUTxO{}), OutputTooSmall},
		{"output", conway(&ledger.OutputTooSmallUtxo{}), OutputTooSmall},
		{"too big", conway(&ledger.OutputTooBigUtxo{}), OutputTooBig},
		{"boot attrs", conway(&ledger.OutputBootAddrAttrsTooBig{}), OutputBootAddrAttrs},
		{"max size", conway(&ledger.MaxTxSizeUtxo{}), MaxTxSize},
		{"empty set", conway(&ledger.InputSetEmptyUtxo{}), InputSetEmpty},
		{"network", conway(&ledger.WrongNetwork{}), WrongNetwork},
		{"withdrawal network", conway(&ledger.WrongNetworkWithdrawal{}), WrongNetworkWithdrawal},
		{"body network", conway(&ledger.WrongNetworkInTxBody{}), WrongNetworkInTxBody},
		{"forecast", conway(&ledger.OutsideForecast{}), OutsideForecast},
		{"non-disjoint", conway(&ledger.BabbageNonDisjointRefInputs{}), NonDisjointRefInputs},
		{"era", localtxsubmission.TransactionRejectedError{Reason: &ledger.EraMismatch{}}, EraMismatch},
		{"vkey", localtxsubmission.TransactionRejectedError{Reason: &ledger.ShelleyTxValidationError{
			Era: 6, Err: ledger.ApplyTxError{Failures: []error{&ledger.UtxowFailure{Err: &ledger.MissingVKeyWitnessesUTXOW{}}}},
		}}, MissingVKeyWitnesses},
		{"invalid witness", localtxsubmission.TransactionRejectedError{Reason: &ledger.ShelleyTxValidationError{
			Era: 6, Err: ledger.ApplyTxError{Failures: []error{&ledger.UtxowFailure{Err: &ledger.InvalidWitnessesUTXOW{}}}},
		}}, InvalidWitnesses},
		{"script missing", localtxsubmission.TransactionRejectedError{Reason: &ledger.ShelleyTxValidationError{
			Era: 6, Err: ledger.ApplyTxError{Failures: []error{&ledger.UtxowFailure{Err: &ledger.MissingScriptWitnessesUTXOW{}}}},
		}}, MissingScriptWitnesses},
		{"script invalid", localtxsubmission.TransactionRejectedError{Reason: &ledger.ShelleyTxValidationError{
			Era: 6, Err: ledger.ApplyTxError{Failures: []error{&ledger.UtxowFailure{Err: &ledger.ScriptWitnessNotValidatingUTXOW{}}}},
		}}, ScriptWitnessNotValid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := outcomeOf("n", tt.err)
			if o.Verdict != Rejected || len(o.Failures) != 1 || o.Failures[0].Class != tt.want {
				t.Fatalf("outcome = %+v, want one %s", o, tt.want)
			}
		})
	}
}

func TestClassifyKeepsEveryFailureAndUnknowns(t *testing.T) {
	err := localtxsubmission.TransactionRejectedError{Reason: &ledger.ShelleyTxValidationError{
		Era: 6, Err: ledger.ApplyTxError{Failures: []error{
			&ledger.UtxowFailure{Err: &ledger.UtxoFailure{Era: 6, Err: &ledger.BadInputsUtxo{}}},
			&ledger.UtxowFailure{Err: &ledger.UtxoFailure{Era: 6, Err: &ledger.ValueNotConservedUtxo{}}},
			&ledger.UnknownApplyTxFailureError{Era: 6, FailureType: 77},
			errors.New("something new"),
		}},
	}}
	o := outcomeOf("n", err)
	got := []Class{}
	for _, f := range o.Failures {
		got = append(got, f.Class)
	}
	want := []Class{BadInputs, ValueNotConserved, Unknown, Unknown}
	if len(got) != len(want) {
		t.Fatalf("classes = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("classes = %v, want %v", got, want)
		}
	}
	if o.Failures[3].Detail != "something new" {
		t.Fatalf("unknown detail dropped: %+v", o.Failures[3])
	}
}

func TestClassifyUndecodedRejectionKept(t *testing.T) {
	o := outcomeOf("n", localtxsubmission.TransactionRejectedError{ReasonCbor: []byte{0x01}})
	if o.Verdict != Rejected || len(o.Failures) != 1 || o.Failures[0].Class != Unknown || o.Failures[0].Detail == "" {
		t.Fatalf("outcome = %+v", o)
	}
}

func TestClassifyMempoolFailure(t *testing.T) {
	raw, err := cbor.Encode([]any{7, "All inputs are spent."})
	if err != nil {
		t.Fatal(err)
	}
	e := localtxsubmission.TransactionRejectedError{Reason: &ledger.ShelleyTxValidationError{
		Era: 6, Err: ledger.ApplyTxError{Failures: []error{&ledger.UnknownApplyTxFailureError{Era: 6, FailureType: 7, Cbor: raw}}},
	}}
	o := outcomeOf("n", e)
	if o.Verdict != Rejected || len(o.Failures) != 1 || o.Failures[0].Class != MempoolFailure ||
		!strings.Contains(o.Failures[0].Detail, "All inputs are spent.") {
		t.Fatalf("outcome = %+v", o)
	}
}
