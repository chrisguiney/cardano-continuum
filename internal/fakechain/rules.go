package fakechain

import (
	"errors"
	"fmt"
	"strings"

	"github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/gouroboros/ledger/conway"
	"github.com/blinklabs-io/gouroboros/ledger/shelley"
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
	ExtraRedeemers          Class = "ExtraRedeemers"
	MissingRedeemers        Class = "MissingRedeemers"
	ScriptIntegrityHash     Class = "PPViewHashesDontMatch"
	NotAllowedSupplemental  Class = "NotAllowedSupplementalDatums"
	ValidationTagMismatch   Class = "ValidationTagMismatch"
	InsufficientCollateral  Class = "InsufficientCollateral"
	NoCollateralInputs      Class = "NoCollateralInputs"
	CollateralNonADA        Class = "CollateralContainsNonADA"
	ExUnitsTooBig           Class = "ExUnitsTooBigUTxO"
)

// Certificate and governance failure classes. Conway reports these from the
// CERTS, DELEG, POOL, GOVCERT and GOV rules
// (eras/conway/impl/src/Cardano/Ledger/Conway/Rules/{Deleg,GovCert,Gov}.hs and
// eras/shelley/impl/src/Cardano/Ledger/Shelley/Rules/Pool.hs).
const (
	StakeKeyRegistered           Class = "StakeKeyRegisteredDELEG"
	StakeKeyNotRegistered        Class = "StakeKeyNotRegisteredDELEG"
	DelegateeNotRegistered       Class = "DelegateeStakePoolNotRegisteredDELEG"
	DelegateeDRepNotRegistered   Class = "DelegateeDRepNotRegisteredDELEG"
	IncorrectDeposit             Class = "IncorrectDepositDELEG"
	RefundIncorrect              Class = "RefundIncorrectDELEG"
	DRepAlreadyRegistered        Class = "ConwayDRepAlreadyRegistered"
	DRepNotRegistered            Class = "ConwayDRepNotRegistered"
	CommitteeIsUnknown           Class = "ConwayCommitteeIsUnknown"
	PoolNotRegistered            Class = "StakePoolNotRegisteredOnKeyPOOL"
	PoolRetirementWrongEpoch     Class = "StakePoolRetirementWrongEpochPOOL"
	PoolCostTooLow               Class = "StakePoolCostTooLowPOOL"
	WithdrawalsNotInRewards      Class = "WithdrawalsNotInRewardsCERTS"
	WdrlNotDelegatedToDRep       Class = "ConwayWdrlNotDelegatedToDRep"
	VotersDoNotExist             Class = "VotersDoNotExist"
	GovActionsDoNotExist         Class = "GovActionsDoNotExist"
	VotingOnExpired              Class = "VotingOnExpiredGovAction"
	ProposalDepositIncorrect     Class = "ProposalDepositIncorrect"
	ProposalReturnAccountMissing Class = "ProposalReturnAccountDoesNotExist"
	DisallowedVoters             Class = "DisallowedVoters"
)

// unmappedPrefix marks a failure from a gouroboros rule that has no
// one-to-one cardano-ledger name. It is reported rather than dropped so a
// rejection is never silently weaker than the rule set that produced it.
const unmappedPrefix = "gouroboros:"

var classByRule = map[common.UtxoValidationRuleId]Class{
	common.UtxoValidationRuleBadInputs:                   BadInputs,
	common.UtxoValidationRuleFeeTooSmall:                 FeeTooSmall,
	common.UtxoValidationRuleValueNotConserved:           ValueNotConserved,
	common.UtxoValidationRuleOutsideValidityInterval:     OutsideValidityInterval,
	common.UtxoValidationRuleOutputTooSmall:              OutputTooSmall,
	common.UtxoValidationRuleOutputTooBig:                OutputTooBig,
	common.UtxoValidationRuleOutputBootAddrAttrsTooBig:   OutputBootAddrAttrs,
	common.UtxoValidationRuleMaxTxSize:                   MaxTxSize,
	common.UtxoValidationRuleInputSetEmpty:               InputSetEmpty,
	common.UtxoValidationRuleWrongNetwork:                WrongNetwork,
	common.UtxoValidationRuleWrongNetworkWithdrawal:      WrongNetworkWithdrawal,
	common.UtxoValidationRuleTransactionNetworkId:        WrongNetworkInTxBody,
	common.UtxoValidationRuleOutsideForecast:             OutsideForecast,
	common.UtxoValidationRuleRequiredVKeyWitnesses:       MissingVKeyWitnesses,
	common.UtxoValidationRuleCollateralVKeyWitnesses:     MissingVKeyWitnesses,
	common.UtxoValidationRuleScriptWitnesses:             MissingScriptWitnesses,
	common.UtxoValidationRuleNativeScripts:               ScriptWitnessNotValid,
	common.UtxoValidationRuleDisjointRefInputs:           NonDisjointRefInputs,
	common.UtxoValidationRuleExtraneousRedeemers:         ExtraRedeemers,
	common.UtxoValidationRuleRequiredRedeemers:           MissingRedeemers,
	common.UtxoValidationRuleScriptDataHash:              ScriptIntegrityHash,
	common.UtxoValidationRuleSupplementalDatums:          NotAllowedSupplemental,
	common.UtxoValidationRulePlutusScripts:               ValidationTagMismatch,
	common.UtxoValidationRuleInsufficientCollateral:      InsufficientCollateral,
	common.UtxoValidationRuleNoCollateralInputs:          NoCollateralInputs,
	common.UtxoValidationRuleCollateralContainsNonAda:    CollateralNonADA,
	common.UtxoValidationRuleExUnitsTooBig:               ExUnitsTooBig,
	common.UtxoValidationRuleProposalDeposit:             ProposalDepositIncorrect,
	common.UtxoValidationRuleProposalReturnAccounts:      ProposalReturnAccountMissing,
	common.UtxoValidationRuleUnknownVoters:               VotersDoNotExist,
	common.UtxoValidationRuleUnknownGovActionIds:         GovActionsDoNotExist,
	common.UtxoValidationRuleVotingOnExpiredGovAction:    VotingOnExpired,
	common.UtxoValidationRuleStakePoolVotingRestrictions: DisallowedVoters,
	common.UtxoValidationRuleCCVotingRestrictions:        DisallowedVoters,
	common.UtxoValidationRuleBootstrapVotingRestrictions: DisallowedVoters,
	common.UtxoValidationRuleCommitteeCertificates:       CommitteeIsUnknown,
}

// classByError refines a rule that reports several ledger failures (the
// delegation, deposit, pool and withdrawal rules) by the gouroboros error it
// returned. The first match wins.
var classByError = []errClass{
	errAs[conway.StakeCredentialAlreadyRegisteredError](StakeKeyRegistered),
	errAs[conway.StakeCredentialNotRegisteredError](StakeKeyNotRegistered),
	errAs[shelley.DelegateUnregisteredStakeCredentialError](StakeKeyNotRegistered),
	errAs[shelley.DelegateToUnregisteredPoolError](DelegateeNotRegistered),
	errAs[conway.DelegateVoteToUnregisteredDRepError](DelegateeDRepNotRegistered),
	errAs[conway.CertificateDepositIncorrectError](IncorrectDeposit),
	errAs[conway.CertificateRefundIncorrectError](RefundIncorrect),
	errAs[conway.DRepAlreadyRegisteredError](DRepAlreadyRegistered),
	errAs[conway.DRepNotRegisteredError](DRepNotRegistered),
	errAs[conway.NotCommitteeMemberError](CommitteeIsUnknown),
	errAs[shelley.StakePoolNotRegisteredOnKeyError](PoolNotRegistered),
	errAs[shelley.StakePoolRetirementWrongEpochError](PoolRetirementWrongEpoch),
	errAs[shelley.StakePoolCostTooLowError](PoolCostTooLow),
	errAs[shelley.WithdrawalFromUnregisteredRewardAccountError](WithdrawalsNotInRewards),
	errAs[shelley.IncorrectWithdrawalAmountError](WithdrawalsNotInRewards),
	errAs[conway.WithdrawalNotDelegatedToDRepError](WdrlNotDelegatedToDRep),
}

type errClass struct {
	class Class
	match func(error) bool
}

// errAs matches an error of type T or *T anywhere in the chain: gouroboros
// returns some failures by value and some by pointer.
func errAs[T error](c Class) errClass {
	var match func(error) bool
	match = func(err error) bool {
		switch any(err).(type) {
		case nil:
			return false
		case T, *T:
			return true
		}
		switch u := err.(type) {
		case interface{ Unwrap() error }:
			return match(u.Unwrap())
		case interface{ Unwrap() []error }:
			for _, e := range u.Unwrap() {
				if match(e) {
					return true
				}
			}
		}
		return false
	}
	return errClass{c, match}
}

func refine(c Class, err error) Class {
	for _, e := range classByError {
		if e.match(err) {
			return e.class
		}
	}
	return c
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
	// not model (treasury donations, the current-treasury-value field,
	// phase-2-invalid transactions, PlutusV1 and V2 scripts, another era).
	// Refusing it keeps the fake from accepting what it cannot check.
	ErrUnsupported = errors.New("fakechain: unsupported")
	// ErrMempoolFull: the transaction does not fit in the mempool. A real
	// node blocks the submitter instead; the fake returns at once so a test
	// can observe the condition.
	ErrMempoolFull = errors.New("fakechain: mempool full")
	// ErrNotInMempool: a fault named a transaction the mempool does not hold.
	ErrNotInMempool = errors.New("fakechain: transaction not in mempool")
	// ErrTooDeep: a rollback past the immutable point or genesis.
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
		if d.Id == common.UtxoValidationRuleBadInputs {
			out = append(out, rule{id: auxInputsRule, class: BadInputs, fn: unresolvedAuxInputs})
		}
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

// auxInputsRule identifies the fake's own check that collateral and reference
// inputs resolve.
const auxInputsRule = "badinputs/collateral-reference"

// unresolvedAuxInputs fails when a collateral or reference input is not in the
// UTxO set. The ledger's BadInputsUTxO covers spend, collateral and reference
// inputs together (Babbage Rules/Utxo.hs, babbageUtxoValidation:
// `Shelley.validateBadInputsUTxO utxo allInputs`), but gouroboros' rule checks
// spent inputs only and the rules that do resolve collateral are dropped by
// validate to keep one failure per class.
func unresolvedAuxInputs(tx common.Transaction, _ uint64, ls common.LedgerState, _ common.ProtocolParameters) error {
	var bad []common.TransactionInput
	for _, ins := range [][]common.TransactionInput{tx.Collateral(), tx.ReferenceInputs()} {
		for _, in := range ins {
			if _, err := ls.UtxoById(in); err != nil {
				bad = append(bad, in)
			}
		}
	}
	if len(bad) == 0 {
		return nil
	}
	return shelley.BadInputsUtxoError{Inputs: bad}
}

// validate runs every rule, as the ledger does, and returns all failures.
// The ledger reports BadInputsUTxO once for every unresolved input, so a
// second BadInputs failure is folded into the first.
func validate(tx common.Transaction, slot uint64, ls common.LedgerState, pp common.ProtocolParameters) []Failure {
	var out []Failure
	reported := false
	for _, r := range rules {
		if err := r.fn(tx, slot, ls, pp); err != nil {
			if r.class != BadInputs && resolution(err) {
				continue
			}
			if r.class == BadInputs {
				if reported {
					continue
				}
				reported = true
			}
			out = append(out, Failure{Class: refine(r.class, err), Rule: r.id, Err: err})
		}
	}
	return out
}

// resolution reports whether err is a spent or reference input that did not
// resolve. gouroboros' later rules (required redeemers) re-report it, but the
// ledger names it once, as BadInputsUTxO.
func resolution(err error) bool {
	return errors.Is(err, common.ErrInputResolution) || errors.Is(err, common.ErrReferenceInputResolution)
}

// unsupported names the first feature of tx the fake does not model, or "".
//
// PlutusV1 and V2 are refused because the fake's V1 and V2 cost models are
// ouroboros-mock placeholders, so their budgets would be meaningless.
// Phase-2-invalid transactions are refused because collateral collection is
// not modelled.
func unsupported(tx *conway.ConwayTransaction) string {
	w := tx.Witnesses()
	checks := []struct {
		name string
		used bool
	}{
		{"phase-2-invalid flag", !tx.IsValid()},
		{"treasury donation", tx.Donation() != nil && tx.Donation().Sign() > 0},
		{"current treasury value", tx.CurrentTreasuryValuePresent()},
		{"plutus v1/v2 scripts", w != nil && len(w.PlutusV1Scripts())+len(w.PlutusV2Scripts()) > 0},
	}
	for _, c := range checks {
		if c.used {
			return c.name
		}
	}
	return ""
}
