package build

import (
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"
	"sync"

	"github.com/blinklabs-io/gouroboros/cbor"
	"github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/plutigo/data"
	"github.com/blinklabs-io/plutigo/syn"

	"github.com/chrisguiney/cardano-continuum/internal/contract"
)

// Script is a compiled Plutus script: a validator from the Aiken registry
// (internal/contract) or an instance of one, or a hand-written UPLC
// program for what Aiken cannot express (Burn).
type Script struct {
	// Name names the script: the validator, an instance of a
	// parameterised one ("vesting#3"), or a hand-written script.
	Name string
	// Validator is the registry validator the script is, or is an
	// instance of; empty for hand-written UPLC.
	Validator string
	// Source is the UPLC text a hand-written script is compiled from;
	// empty for a registry script.
	Source string
	// Language is the Plutus language version (1, 2 or 3).
	Language uint8
	// Bytes is the script as it appears in a witness set: the CBOR byte
	// string wrapping the flat-encoded program.
	Bytes []byte
	// flat is the flat-encoded program inside Bytes.
	flat []byte
}

var (
	// AlwaysSucceeds locks funds anyone can spend with any redeemer.
	AlwaysSucceeds = mustValidator("always_succeeds")
	// FreeMint is a minting policy anyone can mint or burn under.
	FreeMint = mustValidator("free_mint")
	// Disbursement is the one disbursement instance (nonce 0): treasury
	// awards locked for milestone draws.
	Disbursement = mustInstance("disbursement", 0)
)

// DisburseContract is the contract index Contract and Template give the
// Disbursement instance: far above any deployment a run makes, so the
// deployed contracts 0, 1, 2, ... never reach it.
const DisburseContract uint64 = 1 << 32

// known maps every script the builder has made, by hash, so a script run
// names its validator and instance (Lookup).
var known sync.Map // common.Blake2b224 -> Script

// Lookup returns the script with hash h, if the builder has made it.
func Lookup(h common.Blake2b224) (Script, bool) {
	s, ok := known.Load(h)
	if !ok {
		return Script{}, false
	}
	return s.(Script), true
}

func remember(s Script) Script {
	known.Store(s.Hash(), s)
	return s
}

func mustValidator(name string) Script {
	v, ok := contract.Named(name)
	if !ok {
		panic("build: no validator " + name + " in the contract registry")
	}

	s, err := fromValidator(v, name)
	if err != nil {
		panic("build: " + err.Error())
	}
	return remember(s)
}

// mustInstance is registry validator name applied to nonce.
func mustInstance(name string, nonce int64) Script {
	v, ok := contract.Named(name)
	if !ok {
		panic("build: no validator " + name + " in the contract registry")
	}

	s, err := fromValidator(v, name, data.NewInteger(big.NewInt(nonce)))
	if err != nil {
		panic("build: " + err.Error())
	}
	return remember(s)
}

// fromValidator makes a Script of v applied to args.
func fromValidator(v contract.Validator, name string, args ...data.PlutusData) (Script, error) {
	code, err := contract.Apply(v, args...)
	if err != nil {
		return Script{}, err
	}

	var flat []byte
	if _, err := cbor.Decode(code, &flat); err != nil {
		return Script{}, fmt.Errorf("unwrap %s: %w", name, err)
	}
	return Script{Name: name, Validator: v.Name, Language: v.Language, Bytes: code, flat: flat}, nil
}

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
	return remember(s)
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

// contracts caches Contract's scripts by index: applying a parameter
// decodes and re-encodes the program, and the engine asks for a
// contract's address often.
var contracts sync.Map // uint64 -> Script

// Template is the registry validator deployed contract i runs. Contract 0
// is the shared AlwaysSucceeds lock; after it the deployments alternate
// between the vesting and escrow templates, so a run with any contracts
// beyond the first calls both.
func Template(i uint64) string {
	switch {
	case i == DisburseContract:
		return Disbursement.Validator
	case i == 0:
		return AlwaysSucceeds.Validator
	case i%2 == 1:
		return "vesting"
	}
	return "escrow"
}

// Contract returns deployed contract i: AlwaysSucceeds for 0,
// Disbursement for DisburseContract, and for
// every other index its template applied to the nonce i, so each has its
// own hash and address. A run deploys contracts 0, 1, 2, ... in order.
func Contract(i uint64) Script {
	switch i {
	case 0:
		return AlwaysSucceeds
	case DisburseContract:
		return Disbursement
	}

	if s, ok := contracts.Load(i); ok {
		return s.(Script)
	}

	name := Template(i)
	v, ok := contract.Named(name)
	if !ok {
		panic("build: no validator " + name + " in the contract registry")
	}

	s, err := fromValidator(v, name+"#"+strconv.FormatUint(i, 10), data.NewInteger(new(big.Int).SetUint64(i)))
	if err != nil {
		// Unreachable: the template takes exactly the nonce, and its
		// flat round-trips (internal/contract's tests).
		panic("build: " + err.Error())
	}
	contracts.Store(i, s)
	return remember(s)
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
	return Script{Name: name, Source: src, Language: 3, Bytes: wrapped, flat: flat}, nil
}

// Flat returns the flat-encoded program.
func (s Script) Flat() []byte { return append([]byte(nil), s.flat...) }

// Witness returns the script as its language's witness-set type.
func (s Script) Witness() common.Script {
	switch s.Language {
	case 1:
		return common.PlutusV1Script(s.Bytes)
	case 2:
		return common.PlutusV2Script(s.Bytes)
	}
	return common.PlutusV3Script(s.Bytes)
}

// Hash returns the script hash, which is also its policy id: the
// language's tag byte and Bytes, hashed by the witness type.
func (s Script) Hash() common.Blake2b224 { return s.Witness().Hash() }

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
