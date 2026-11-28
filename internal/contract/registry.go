package contract

import (
	"encoding/hex"
	"slices"

	"github.com/blinklabs-io/gouroboros/ledger/common"
)

//go:generate go run ./internal/gen -in ../../contracts/templates/plutus.json -out registry_gen.go

// Purpose is what a validator validates: the handler names of CIP-57 and
// Aiken, which the ledger calls script purposes (spending an output,
// minting under a policy, withdrawing rewards, publishing a certificate,
// voting, proposing).
type Purpose string

// The script purposes.
const (
	Spend    Purpose = "spend"
	Mint     Purpose = "mint"
	Withdraw Purpose = "withdraw"
	Publish  Purpose = "publish"
	Vote     Purpose = "vote"
	Propose  Purpose = "propose"
)

// Validator is one compiled validator of the registry.
type Validator struct {
	// Name is the validator's name in its Aiken module ("vesting").
	Name string
	// Module is the source file, relative to its Aiken project.
	Module string
	// Purposes are the purposes it has a handler for.
	Purposes []Purpose
	// Language is the Plutus language version: 3 for PlutusV3.
	Language uint8
	// Hash is the script hash of Code; a parameterised validator's
	// instances each have their own (Apply).
	Hash common.Blake2b224
	// Code is the script as it goes in a witness set: the CBOR byte
	// string wrapping the flat-encoded program.
	Code []byte
	// Params name the parameters Apply takes, in order.
	Params []string
	// Datum names the datum type a spend takes; empty for other purposes.
	Datum string
	// Redeemer names the redeemer type, empty when the handler takes Data.
	Redeemer string
}

// Size is the script's size in bytes in a witness set.
func (v Validator) Size() int { return len(v.Code) }

// All returns the registry's validators by name.
func All() []Validator { return slices.Clone(registry) }

// Named returns the validator called name.
func Named(name string) (Validator, bool) {
	i := slices.IndexFunc(registry, func(v Validator) bool { return v.Name == name })
	if i < 0 {
		return Validator{}, false
	}
	return registry[i], true
}

// mustHex decodes the generated file's hex literals.
func mustHex(s string) []byte {
	b, err := hex.DecodeString(s)
	if err != nil {
		panic("contract: generated hex: " + err.Error())
	}
	return b
}

// mustHash decodes a generated script hash.
func mustHash(s string) common.Blake2b224 {
	var h common.Blake2b224
	if b := mustHex(s); len(b) == len(h) {
		copy(h[:], b)
		return h
	}
	panic("contract: generated hash " + s + " is not 28 bytes")
}
