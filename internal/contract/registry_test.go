package contract

import (
	"bytes"
	"encoding/hex"
	"os"
	"slices"
	"testing"

	"github.com/blinklabs-io/gouroboros/cbor"
	"github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/plutigo/syn"
)

const blueprintPath = "../../contracts/templates/plutus.json"

func readBlueprint(t *testing.T) Blueprint {
	t.Helper()
	raw, err := os.ReadFile(blueprintPath)
	if err != nil {
		t.Fatal(err)
	}
	bp, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return bp
}

// The registry is generated from the committed blueprint; rendering it
// again must give the committed file byte for byte.
func TestRegistryMatchesBlueprint(t *testing.T) {
	t.Parallel()
	got, err := Render(readBlueprint(t), "contracts/templates/plutus.json")
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("registry_gen.go")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("registry_gen.go drifts from contracts/templates/plutus.json: run make contracts")
	}
}

// Aiken hashes scripts with pallas; the ledger rules and the builder hash
// them with gouroboros. The two must agree on every validator, or a
// script's address and policy id differ between compiler and chain.
func TestBlueprintHashIsGouroborosHash(t *testing.T) {
	t.Parallel()
	bp := readBlueprint(t)
	if len(bp.Validators) == 0 {
		t.Fatal("blueprint has no validators")
	}
	for _, e := range bp.Validators {
		code, err := hex.DecodeString(e.CompiledCode)
		if err != nil {
			t.Fatal(err)
		}
		if got := common.PlutusV3Script(code).Hash().String(); got != e.Hash {
			t.Errorf("%s: gouroboros hash %s, blueprint %s", e.Title, got, e.Hash)
		}
	}
	for _, v := range All() {
		if got := common.PlutusV3Script(v.Code).Hash(); got != v.Hash {
			t.Errorf("%s: gouroboros hash %s, registry %s", v.Name, got, v.Hash)
		}
	}
}

// Parameter application decodes and re-encodes Aiken's flat output, so
// plutigo's flat codec must round-trip every validator byte for byte.
func TestFlatRoundTrip(t *testing.T) {
	t.Parallel()
	for _, v := range All() {
		var flat []byte
		if _, err := cbor.Decode(v.Code, &flat); err != nil {
			t.Fatalf("%s: unwrap: %v", v.Name, err)
		}
		prog, err := syn.Decode[syn.DeBruijn](flat)
		if err != nil {
			t.Fatalf("%s: decode: %v", v.Name, err)
		}
		again, err := syn.Encode(prog)
		if err != nil {
			t.Fatalf("%s: encode: %v", v.Name, err)
		}
		if !bytes.Equal(again, flat) {
			t.Errorf("%s: flat does not round-trip", v.Name)
		}
	}
}

func TestRegistryEntries(t *testing.T) {
	t.Parallel()
	want := map[string][]Purpose{
		"always_succeeds": {Spend},
		"disbursement":    {Spend},
		"escrow":          {Spend},
		"free_mint":       {Mint},
		"vesting":         {Spend},
	}
	var names []string
	for _, v := range All() {
		names = append(names, v.Name)
		if !slices.Equal(v.Purposes, want[v.Name]) {
			t.Errorf("%s: purposes %v, want %v", v.Name, v.Purposes, want[v.Name])
		}
		if v.Language != 3 {
			t.Errorf("%s: language %d, want 3", v.Name, v.Language)
		}
		if v.Size() != len(v.Code) || v.Size() == 0 {
			t.Errorf("%s: size %d of %d code bytes", v.Name, v.Size(), len(v.Code))
		}
		if v.Module != "validators/"+v.Name+".ak" {
			t.Errorf("%s: module %q", v.Name, v.Module)
		}
		got, ok := Named(v.Name)
		if !ok || got.Hash != v.Hash {
			t.Errorf("Named(%q) = %v, %v", v.Name, got.Hash, ok)
		}
	}
	if !slices.IsSorted(names) || len(names) != len(want) {
		t.Fatalf("registry names %v", names)
	}
	vest, _ := Named("vesting")
	if !slices.Equal(vest.Params, []string{"_nonce"}) {
		t.Fatalf("vesting params %v", vest.Params)
	}
	esc, _ := Named("escrow")
	if vest.Datum != "continuum/types/VestingDatum" || esc.Datum != "continuum/types/EscrowDatum" || esc.Redeemer != "continuum/types/EscrowAction" || vest.Redeemer != "" {
		t.Fatalf("schemas: vesting %q %q, escrow %q %q", vest.Datum, vest.Redeemer, esc.Datum, esc.Redeemer)
	}
	if _, ok := Named("nope"); ok {
		t.Fatal("Named found an unknown validator")
	}
}

func TestParseRejects(t *testing.T) {
	t.Parallel()
	for name, raw := range map[string]string{
		"not json":     "{",
		"no plutus":    `{"preamble":{},"validators":[{"title":"a.b.spend","compiledCode":"00","hash":"00"}]}`,
		"bad title":    `{"preamble":{"plutusVersion":"v3"},"validators":[{"title":"ab","compiledCode":"00","hash":"00"}]}`,
		"bad code hex": `{"preamble":{"plutusVersion":"v3"},"validators":[{"title":"a.b.spend","compiledCode":"zz","hash":"00"}]}`,
		"bad purpose":  `{"preamble":{"plutusVersion":"v3"},"validators":[{"title":"a.b.steal","compiledCode":"00","hash":"00"}]}`,
	} {
		if _, err := Parse([]byte(raw)); err == nil {
			t.Errorf("%s: parsed", name)
		}
	}
	if _, err := Parse(bytes.Repeat([]byte(" "), maxBlueprint+1)); err == nil {
		t.Error("an oversize blueprint parsed")
	}
}
