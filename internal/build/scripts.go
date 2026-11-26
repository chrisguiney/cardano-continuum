package build

import (
	"encoding/hex"
	"fmt"
	"math"
	"strconv"
	"strings"
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

// burns caches Burn's compiled policies by loop size.
var burns sync.Map // int -> Script

// Burn returns a minting policy that accepts after a builtin-free loop of
// about n*n applications, so it spends most of the execution units its
// redeemer declares (BurnSize) instead of merely claiming them. Every CEK step costs 16000 CPU
// and 100 memory units in the PV10 cost model (cek*Cost'exBudget*), so the
// loop's memory is close to steps/160: a budget the steps limit allows is a
// memory budget it can actually spend. The loop applies the Church numeral
// n to the Church numeral n's application to the identity, then to unit.
func Burn(n int) Script {
	if s, ok := burns.Load(n); ok {
		return s.(Script)
	}
	num := numeral(max(n, 1))
	s := mustCompile("burn-"+strconv.Itoa(n),
		"(program 1.1.0 (lam ctx [["+num+" ["+num+" (lam y y)]] (con unit ())]))")
	burns.Store(n, s)
	return s
}

// burnMemory is the memory a built Burn(n) declares per n*n: it spends
// about 300*n*n on plutigo with the PV10 cost model (3,031,400 at n = 100),
// and Apollo declares 20% above what it measures (apollo.go
// ExMemoryBuffer, ExStepBuffer), so the script spends 5/6 of what it
// declares.
const burnMemory = 360

// BurnSize returns the loop size whose Burn, built by the Builder,
// declares about mem memory units.
func BurnSize(mem int64) int {
	return int(math.Sqrt(float64(max(mem, 0)) / burnMemory))
}

// numeral is the Church numeral n: (lam f (lam x [f [f ... x]])).
func numeral(n int) string {
	var b strings.Builder
	b.WriteString("(lam f (lam x ")
	for range n {
		b.WriteString("[f ")
	}
	b.WriteString("x")
	b.WriteString(strings.Repeat("]", n))
	b.WriteString("))")
	return b.String()
}

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
