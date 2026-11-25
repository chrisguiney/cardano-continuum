package fakechain

import (
	"errors"
	"fmt"
	"strings"

	"github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/gouroboros/ledger/conway"
)

// Class names a ledger predicate failure the way cardano-ledger names it in a
// node's rejection (the constructor of the Conway UTXO or UTXOW failure), so a
// verdict from the fake compares directly with one decoded from a real node.
type Class string

// Ledger predicate failure classes the fake reports. Names follow the Conway
// era's predicate failure constructors in cardano-ledger
// (eras/conway/impl/src/Cardano/Ledger/Conway/Rules/Utxo.hs and Utxow.hs).
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
)

// unmappedPrefix marks a failure from a gouroboros rule that has no
// one-to-one cardano-ledger name. It is reported rather than dropped so a
// rejection is never silently weaker than the rule set that produced it.
const unmappedPrefix = "gouroboros:"

var classByRule = map[common.UtxoValidationRuleId]Class{
	common.UtxoValidationRuleBadInputs:                 BadInputs,
	common.UtxoValidationRuleFeeTooSmall:               FeeTooSmall,
	common.UtxoValidationRuleValueNotConserved:         ValueNotConserved,
	common.UtxoValidationRuleOutsideValidityInterval:   OutsideValidityInterval,
	common.UtxoValidationRuleOutputTooSmall:            OutputTooSmall,
	common.UtxoValidationRuleOutputTooBig:              OutputTooBig,
	common.UtxoValidationRuleOutputBootAddrAttrsTooBig: OutputBootAddrAttrs,
	common.UtxoValidationRuleMaxTxSize:                 MaxTxSize,
	common.UtxoValidationRuleInputSetEmpty:             InputSetEmpty,
	common.UtxoValidationRuleWrongNetwork:              WrongNetwork,
	common.UtxoValidationRuleWrongNetworkWithdrawal:    WrongNetworkWithdrawal,
	common.UtxoValidationRuleTransactionNetworkId:      WrongNetworkInTxBody,
	common.UtxoValidationRuleOutsideForecast:           OutsideForecast,
	common.UtxoValidationRuleRequiredVKeyWitnesses:     MissingVKeyWitnesses,
	common.UtxoValidationRuleCollateralVKeyWitnesses:   MissingVKeyWitnesses,
	common.UtxoValidationRuleScriptWitnesses:           MissingScriptWitnesses,
	common.UtxoValidationRuleNativeScripts:             ScriptWitnessNotValid,
	common.UtxoValidationRuleDisjointRefInputs:         NonDisjointRefInputs,
}

// Unmapped reports whether c came from a gouroboros rule with no
// cardano-ledger name in this package.
func (c Class) Unmapped() bool { return strings.HasPrefix(string(c), unmappedPrefix) }

// Failure is one failed ledger predicate.
type Failure struct {
	Class Class
	// Rule is the gouroboros rule identifier that failed.
	Rule string
	Err  error
}

// RejectError is the fake's equivalent of a node's ApplyTxError: every
// predicate the transaction failed, in rule order.
type RejectError struct {
	Tx       TxID
	Failures []Failure
}

func (e *RejectError) Error() string {
	cs := make([]string, len(e.Failures))
	for i, f := range e.Failures {
		cs[i] = string(f.Class)
	}
	return fmt.Sprintf("fakechain: tx %s rejected: %s", e.Tx, strings.Join(cs, ", "))
}

// Has reports whether c is among the failures.
func (e *RejectError) Has(c Class) bool {
	for _, f := range e.Failures {
		if f.Class == c {
			return true
		}
	}
	return false
}

// Errors outside the ledger's predicate failures.
var (
	// ErrDecode: the bytes are not a Conway transaction. A node fails such a
	// submission in the protocol's deserialiser, not in the ledger.
	ErrDecode = errors.New("fakechain: transaction does not decode")
	// ErrUnsupported: a well-formed transaction uses a feature the fake does
	// not model (certificates, withdrawals, minting, governance, Plutus,
	// collateral, another era). Refusing it keeps the fake from accepting
	// what it cannot check.
	ErrUnsupported = errors.New("fakechain: unsupported")
	// ErrMempoolFull: the transaction does not fit in the mempool. A real
	// node blocks the submitter instead; the fake returns at once so a test
	// can observe the condition.
	ErrMempoolFull = errors.New("fakechain: mempool full")
	// ErrNotInMempool: a fault named a transaction the mempool does not hold.
	ErrNotInMempool = errors.New("fakechain: transaction not in mempool")
	// ErrTooDeep: a rollback deeper than k or past genesis.
	ErrTooDeep = errors.New("fakechain: rollback too deep")
)

type rule struct {
	id    string
	class Class
	fn    common.UtxoValidationRuleFunc
}

// rules is gouroboros' authoritative Conway rule list, in its order, with the
// signatures rule split in two: gouroboros checks signature validity and
// witness presence in one rule, while the ledger reports them as separate
// failures (InvalidWitnessesUTXOW, MissingVKeyWitnessesUTXOW).
var rules = conwayRules()

func conwayRules() []rule {
	var out []rule
	for _, d := range conway.UtxoValidationRuleDescriptors() {
		if d.Id == common.UtxoValidationRuleSignatures {
			out = append(out, signatureRules()...)
			continue
		}
		c, ok := classByRule[d.Id]
		if !ok {
			c = Class(unmappedPrefix + string(d.Id))
		}
		out = append(out, rule{id: string(d.Id), class: c, fn: d.Validator})
	}
	return out
}

// signatureRules restates common.UtxoValidateSignatures as its three parts.
// Witness signatures are checked with common.VerifyVKeySignature inside
// common.ValidateVKeyWitnesses.
func signatureRules() []rule {
	return []rule{
		{"signatures/vkey", InvalidWitnesses, func(tx common.Transaction, _ uint64, _ common.LedgerState, _ common.ProtocolParameters) error {
			return common.ValidateVKeyWitnesses(tx)
		}},
		{"signatures/bootstrap", InvalidWitnesses, func(tx common.Transaction, _ uint64, _ common.LedgerState, _ common.ProtocolParameters) error {
			return common.ValidateBootstrapWitnesses(tx)
		}},
		{"signatures/inputs", MissingVKeyWitnesses, func(tx common.Transaction, _ uint64, ls common.LedgerState, _ common.ProtocolParameters) error {
			return common.ValidateInputVKeyWitnesses(tx, ls)
		}},
	}
}

// validate runs every rule, as the ledger does, and returns all failures.
func validate(tx common.Transaction, slot uint64, ls common.LedgerState, pp common.ProtocolParameters) []Failure {
	var out []Failure
	for _, r := range rules {
		if err := r.fn(tx, slot, ls, pp); err != nil {
			out = append(out, Failure{Class: r.class, Rule: r.id, Err: err})
		}
	}
	return out
}

// unsupported names the first feature of tx the fake does not model, or "".
func unsupported(tx *conway.ConwayTransaction) string {
	w := tx.Witnesses()
	checks := []struct {
		name string
		used bool
	}{
		{"phase-2-invalid flag", !tx.IsValid()},
		{"certificates", len(tx.Certificates()) > 0},
		{"withdrawals", len(tx.Withdrawals()) > 0},
		{"minting", tx.AssetMint() != nil},
		{"voting procedures", len(tx.VotingProcedures()) > 0},
		{"proposal procedures", len(tx.ProposalProcedures()) > 0},
		{"treasury donation", tx.Donation() != nil && tx.Donation().Sign() > 0},
		{"current treasury value", tx.CurrentTreasuryValuePresent()},
		{"collateral", len(tx.Collateral()) > 0},
		{"redeemers", w != nil && w.Redeemers() != nil && redeemerCount(w) > 0},
		{"plutus scripts", w != nil && len(w.PlutusV1Scripts())+len(w.PlutusV2Scripts())+len(w.PlutusV3Scripts()) > 0},
	}
	for _, c := range checks {
		if c.used {
			return c.name
		}
	}
	return ""
}

func redeemerCount(w common.TransactionWitnessSet) int {
	n := 0
	for range w.Redeemers().Iter() {
		n++
	}
	return n
}
