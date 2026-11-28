package contract

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// maxBlueprint bounds the plutus.json Parse reads: a few validators are a
// few KiB, and the ledger caps a script at 16 KiB.
const maxBlueprint = 4 << 20

// Blueprint is the part of a CIP-57 plutus.json the registry needs.
type Blueprint struct {
	// Preamble names the project, its Plutus version and the compiler.
	Preamble Preamble `json:"preamble"`
	// Validators has one entry per validator handler: Aiken titles them
	// module.validator.purpose, with "else" for the fallback handler.
	Validators []Entry `json:"validators"`
	// Definitions are the schemas of the types the validators' datums,
	// redeemers and parameters use, by name ("continuum/types/EscrowDatum").
	Definitions map[string]Definition `json:"definitions"`
}

// Preamble is a blueprint's header.
type Preamble struct {
	// Title is the Aiken project name.
	Title string `json:"title"`
	// PlutusVersion is the language every validator targets ("v3").
	PlutusVersion string `json:"plutusVersion"`
	// Compiler names the compiler and its version.
	Compiler Compiler `json:"compiler"`
}

// Compiler names the compiler that wrote a blueprint.
type Compiler struct {
	// Name is "Aiken".
	Name string `json:"name"`
	// Version is the compiler version and commit, "v1.1.24+bacbeb3".
	Version string `json:"version"`
}

// Entry is one validator handler of a blueprint.
type Entry struct {
	// Title is module.validator.purpose.
	Title string `json:"title"`
	// CompiledCode is the hex of the CBOR byte string wrapping the
	// flat-encoded program, as it goes in a witness set.
	CompiledCode string `json:"compiledCode"`
	// Hash is the script hash Aiken computed, in hex.
	Hash string `json:"hash"`
	// Parameters are the validator's parameters, applied before use.
	Parameters []Arg `json:"parameters,omitempty"`
	// Datum is a spend handler's datum schema.
	Datum *Arg `json:"datum,omitempty"`
	// Redeemer is the handler's redeemer schema.
	Redeemer *Arg `json:"redeemer,omitempty"`
}

// Arg is a named argument and its schema reference.
type Arg struct {
	// Title is the argument's name in the source.
	Title string `json:"title"`
	// Schema holds the reference to the argument's type definition.
	Schema Schema `json:"schema"`
}

// Schema is the reference form CIP-57 uses for named types.
type Schema struct {
	// Ref is "#/definitions/<type>", with '/' in the type escaped as "~1".
	Ref string `json:"$ref"`
}

// TypeName is the type the schema references, "continuum/types/EscrowDatum".
func (s Schema) TypeName() string {
	t := strings.TrimPrefix(s.Ref, "#/definitions/")
	return strings.ReplaceAll(t, "~1", "/")
}

// purposes are the handler names CIP-57 allows, in the ledger's order,
// plus Aiken's fallback.
var purposes = map[string]bool{"spend": true, "mint": true, "withdraw": true, "publish": true, "vote": true, "propose": true, "else": true}

// Parse reads and validates a plutus.json.
func Parse(raw []byte) (Blueprint, error) {
	if len(raw) > maxBlueprint {
		return Blueprint{}, fmt.Errorf("contract: blueprint is %d bytes, over %d", len(raw), maxBlueprint)
	}

	var bp Blueprint
	if err := json.Unmarshal(raw, &bp); err != nil {
		return Blueprint{}, fmt.Errorf("contract: blueprint: %w", err)
	}

	if bp.Preamble.PlutusVersion != "v3" {
		return Blueprint{}, fmt.Errorf("contract: blueprint plutus version %q, want v3", bp.Preamble.PlutusVersion)
	}

	for _, e := range bp.Validators {
		if err := e.check(); err != nil {
			return Blueprint{}, fmt.Errorf("contract: validator %q: %w", e.Title, err)
		}
	}
	return bp, nil
}

func (e Entry) check() error {
	parts := strings.Split(e.Title, ".")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" {
		return errors.New("title is not module.validator.purpose")
	}

	if !purposes[parts[2]] {
		return fmt.Errorf("unknown purpose %q", parts[2])
	}

	if _, err := hex.DecodeString(e.CompiledCode); err != nil || e.CompiledCode == "" {
		return errors.New("compiled code is not hex")
	}

	if h, err := hex.DecodeString(e.Hash); err != nil || len(h) == 0 {
		return errors.New("hash is not hex")
	}
	return nil
}

// split returns the entry's module, validator and purpose.
func (e Entry) split() (module, validator, purpose string) {
	parts := strings.Split(e.Title, ".")
	return parts[0], parts[1], parts[2]
}
