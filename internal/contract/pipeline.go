package contract

import (
	"encoding/hex"
	"fmt"

	"github.com/blinklabs-io/gouroboros/cbor"
	"github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/plutigo/data"
	"github.com/blinklabs-io/plutigo/syn"
)

// Pipeline is a script at each phase of its compilation, computed from the
// committed code alone, so it needs no Aiken: the Aiken source compiles
// to UPLC, which is flat-encoded, CBOR-wrapped and hashed.
type Pipeline struct {
	// UPLC is the program in the textual UPLC syntax, with each variable named for its lambda (v0, v1, ...); the flat form numbers them instead.
	UPLC string `json:"uplc"`
	// Flat is the flat-encoded program in hex.
	Flat string `json:"flat"`
	// FlatSize is the flat encoding's size in bytes.
	FlatSize int `json:"flat_size"`
	// Wrapped is the CBOR byte string around the flat bytes in hex: the
	// script as a witness set carries it, and the bytes the hash covers
	// after the tag.
	Wrapped string `json:"wrapped"`
	// WrappedSize is its size in bytes.
	WrappedSize int `json:"wrapped_size"`
	// Tag is the language byte hashed before Wrapped: 1, 2 or 3 for
	// PlutusV1, V2 or V3.
	Tag uint8 `json:"tag"`
	// Hash is blake2b-224 of Tag followed by Wrapped, in hex: the script
	// hash, which is the script's address and a minting policy's id.
	Hash string `json:"hash"`
}

// Pipe returns v's phases after applying it to args, one Data per
// parameter; with none it is the compiler's output, parameters unapplied.
func Pipe(v Validator, args ...data.PlutusData) (Pipeline, error) {
	code := v.Code
	if len(args) > 0 {
		var err error
		if code, err = Apply(v, args...); err != nil {
			return Pipeline{}, err
		}
	}

	var flat []byte
	if _, err := cbor.Decode(code, &flat); err != nil {
		return Pipeline{}, fmt.Errorf("contract: %s: unwrap: %w", v.Name, err)
	}

	prog, err := syn.Decode[syn.DeBruijn](flat)
	if err != nil {
		return Pipeline{}, fmt.Errorf("contract: %s: decode: %w", v.Name, err)
	}

	nm, err := named(prog)
	if err != nil {
		return Pipeline{}, fmt.Errorf("contract: %s: name: %w", v.Name, err)
	}

	text, err := syn.PrettyWithLimits(nm, syn.PrettyLimits{})
	if err != nil {
		return Pipeline{}, fmt.Errorf("contract: %s: print: %w", v.Name, err)
	}
	return Pipeline{
		UPLC:        text,
		Flat:        hex.EncodeToString(flat),
		FlatSize:    len(flat),
		Wrapped:     hex.EncodeToString(code),
		WrappedSize: len(code),
		Tag:         v.Language,
		Hash:        common.Blake2b224Hash(append([]byte{v.Language}, code...)).String(),
	}, nil
}
