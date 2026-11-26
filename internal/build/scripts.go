package build

import (
	"encoding/hex"
	"fmt"
	"strconv"
	"sync"

	"github.com/blinklabs-io/gouroboros/cbor"
	"github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/plutigo/syn"
)

// Script is a compiled PlutusV3 validator from the builder's registry.
type Script struct {
	Name string
	// Source is the untyped Plutus Core text the script is compiled from.
	Source string
	// V3 is the script as it appears in a witness set: the CBOR byte
	// string wrapping the flat-encoded program.
	V3   common.PlutusV3Script
	flat []byte
}

// The registry. Both scripts ignore their argument and return unit, which
// is how a PlutusV3 script succeeds; FreeMint applies a constant first so
// its hash, and so its policy id, differs from AlwaysSucceeds'.
//
// Contract templates not yet here: always-fails, vesting, escrow, CIP-68
// reference/user token pairs and the order-book DEX.
var (
	// AlwaysSucceeds locks funds anyone can spend with any redeemer.
	AlwaysSucceeds = mustCompile("always-succeeds", "(program 1.1.0 (lam ctx (con unit ())))")
	// FreeMint is a minting policy anyone can mint or burn under.
	FreeMint = mustCompile("free-mint", "(program 1.1.0 [(lam salt (lam ctx (con unit ()))) (con integer 1)])")
)

// contracts caches Contract's compiled scripts by index: compiling parses
// and flat-encodes, and the engine asks for a contract's address often.
var contracts sync.Map // uint64 -> Script

// Contract returns deployed contract i: AlwaysSucceeds for 0, and for
// every other index the same validator applied to a salt naming i, so
// each has its own hash and address. A run deploys contracts 0, 1, 2, ...
// in order.
func Contract(i uint64) Script {
	if i == 0 {
		return AlwaysSucceeds
	}
	if s, ok := contracts.Load(i); ok {
		return s.(Script)
	}
	salt := hex.EncodeToString([]byte("contract-" + strconv.FormatUint(i, 10)))
	s := mustCompile("contract-"+strconv.FormatUint(i, 10),
		"(program 1.1.0 [(lam salt (lam ctx (con unit ()))) (con bytestring #"+salt+")])")
	contracts.Store(i, s)
	return s
}

func mustCompile(name, src string) Script {
	s, err := compile(name, src)
	if err != nil {
		panic("build: compile embedded script " + name + ": " + err.Error())
	}
	return s
}

// compile parses UPLC source with plutigo and flat-encodes it.
func compile(name, src string) (Script, error) {
	prog, err := syn.Parse(src)
	if err != nil {
		return Script{}, fmt.Errorf("parse: %w", err)
	}
	db, err := syn.NameToDeBruijn(prog)
	if err != nil {
		return Script{}, fmt.Errorf("de bruijn: %w", err)
	}
	flat, err := syn.Encode(db)
	if err != nil {
		return Script{}, fmt.Errorf("flat: %w", err)
	}
	wrapped, err := cbor.Encode(flat)
	if err != nil {
		return Script{}, fmt.Errorf("cbor: %w", err)
	}
	return Script{Name: name, Source: src, V3: common.PlutusV3Script(wrapped), flat: flat}, nil
}

// Flat returns the flat-encoded program.
func (s Script) Flat() []byte { return append([]byte(nil), s.flat...) }

// Hash returns the script hash, which is also its policy id.
func (s Script) Hash() common.Blake2b224 { return s.V3.Hash() }

// Address returns the enterprise address locked by the script.
func (s Script) Address(networkID uint8) common.Address {
	h := s.Hash()
	a, err := common.NewAddressFromParts(common.AddressTypeScriptNone, networkID, h[:], nil)
	if err != nil {
		// Unreachable: the hash is 28 bytes and network ids are 4 bits.
		panic("build: script address: " + err.Error())
	}
	return a
}
