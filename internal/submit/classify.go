package submit

import (
	"errors"
	"fmt"

	"github.com/blinklabs-io/gouroboros/cbor"
	"github.com/blinklabs-io/gouroboros/ledger"
	"github.com/blinklabs-io/gouroboros/protocol/localtxsubmission"
)

// conwayMempoolFailure is the LEDGER constructor tag of ConwayMempoolFailure
// (Cardano.Ledger.Conway.Rules.Ledger), which carries free text. A node
// raises it before the UTXO rules, for example when every input is already
// spent by a transaction in its mempool.
const conwayMempoolFailure = 7

// ledgerRejection decodes a local-tx-submission rejection into failures.
func ledgerRejection(err error) ([]Failure, bool) {
	var rej localtxsubmission.TransactionRejectedError
	if !errors.As(err, &rej) {
		return nil, false
	}
	if rej.Reason == nil {
		return []Failure{{Class: Unknown, Detail: rej.Error()}}, true
	}
	return walk(rej.Reason), true
}

// walk flattens a decoded rejection tree into one Failure per leaf. The
// wrappers mirror the nesting a node's ApplyTxError carries (LEDGER, UTXOW,
// UTXO). A leaf with no mapping is kept as Unknown with its text.
func walk(err error) []Failure {
	switch e := err.(type) {
	case nil:
		return []Failure{{Class: Unknown, Detail: "empty rejection"}}
	case *ledger.ShelleyTxValidationError:
		return walk(&e.Err)
	case *ledger.ApplyTxError:
		var out []Failure
		for _, f := range e.Failures {
			out = append(out, walk(f)...)
		}
		if len(out) == 0 {
			return []Failure{{Class: Unknown, Detail: "rejection with no failures"}}
		}
		return out
	case *ledger.UtxowFailure:
		return walk(e.Err)
	case *ledger.ConwayUtxowFailure:
		return walk(e.Err)
	case *ledger.ShelleyUtxowFailure:
		return walk(e.Err)
	case *ledger.AlonzoUtxowFailure:
		return walk(e.Err)
	case *ledger.BabbageUtxoFailure:
		return walk(e.Err)
	case *ledger.UtxoFailure:
		return walk(e.Err)
	}
	if c, ok := leafClass(err); ok {
		return []Failure{{Class: c, Detail: err.Error()}}
	}
	if u, ok := err.(*ledger.UnknownApplyTxFailureError); ok && u.Era == ledger.EraIdConway && u.FailureType == conwayMempoolFailure {
		return []Failure{{Class: MempoolFailure, Detail: unknownDetail(err)}}
	}
	return []Failure{{Class: Unknown, Detail: unknownDetail(err)}}
}

// unknownDetail is the text of an unmapped failure. The decoder's own
// message for an unrecognised constructor names only the tag, so the raw
// CBOR (and any text payload, as in a MEMPOOL failure) is appended: an
// unmapped rejection must stay diagnosable.
func unknownDetail(err error) string {
	var raw []byte
	switch e := err.(type) {
	case *ledger.UnknownApplyTxFailureError:
		raw = e.Cbor
	case *ledger.UnknownUtxowFailureError:
		raw = e.Cbor
	case *ledger.UnknownUtxoFailureError:
		raw = e.Cbor
	default:
		return err.Error()
	}
	d := fmt.Sprintf("%s cbor=%x", err.Error(), raw)
	var parts []any
	if _, derr := cbor.Decode(raw, &parts); derr == nil {
		for _, p := range parts {
			if txt, ok := p.(string); ok {
				d += " text=" + txt
			}
		}
	}
	return d
}

func leafClass(err error) (Class, bool) {
	switch err.(type) {
	case *ledger.BadInputsUtxo:
		return BadInputs, true
	case *ledger.FeeTooSmallUtxo:
		return FeeTooSmall, true
	case *ledger.ValueNotConservedUtxo:
		return ValueNotConserved, true
	case *ledger.OutsideValidityIntervalUtxo:
		return OutsideValidityInterval, true
	case *ledger.BabbageOutputTooSmallUTxO, *ledger.OutputTooSmallUtxo:
		return OutputTooSmall, true
	case *ledger.OutputTooBigUtxo:
		return OutputTooBig, true
	case *ledger.OutputBootAddrAttrsTooBig:
		return OutputBootAddrAttrs, true
	case *ledger.MaxTxSizeUtxo:
		return MaxTxSize, true
	case *ledger.InputSetEmptyUtxo:
		return InputSetEmpty, true
	case *ledger.WrongNetwork:
		return WrongNetwork, true
	case *ledger.WrongNetworkWithdrawal:
		return WrongNetworkWithdrawal, true
	case *ledger.WrongNetworkInTxBody:
		return WrongNetworkInTxBody, true
	case *ledger.OutsideForecast:
		return OutsideForecast, true
	case *ledger.BabbageNonDisjointRefInputs:
		return NonDisjointRefInputs, true
	case *ledger.MissingVKeyWitnessesUTXOW:
		return MissingVKeyWitnesses, true
	case *ledger.InvalidWitnessesUTXOW:
		return InvalidWitnesses, true
	case *ledger.MissingScriptWitnessesUTXOW:
		return MissingScriptWitnesses, true
	case *ledger.ScriptWitnessNotValidatingUTXOW:
		return ScriptWitnessNotValid, true
	case *ledger.EraMismatch:
		return EraMismatch, true
	}
	return "", false
}
