package contract

import (
	"bytes"
	"encoding/hex"
	"math/big"
	"strings"
	"testing"

	"github.com/blinklabs-io/gouroboros/cbor"
	"github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/plutigo/data"
	"github.com/blinklabs-io/plutigo/syn"
)

// The script hash is blake2b-224 over the language tag and the wrapped
// script as the witness set carries it. The registry hash comes from
// Aiken, so equality with the hash recomputed here ties the phases
// together.
func TestPipelineHashDerivation(t *testing.T) {
	t.Parallel()
	for _, v := range All() {
		p, err := Pipe(v)
		if err != nil {
			t.Fatalf("%s: %v", v.Name, err)
		}
		wrapped, err := hex.DecodeString(p.Wrapped)
		if err != nil {
			t.Fatal(err)
		}
		pre := append([]byte{byte(v.Language)}, wrapped...)
		if got := hex.EncodeToString(common.Blake2b224Hash(pre).Bytes()); got != v.Hash.String() {
			t.Errorf("%s: blake2b224(tag||wrapped) = %s, registry %s", v.Name, got, v.Hash)
		}
		if p.Hash != v.Hash.String() || p.Tag != v.Language {
			t.Errorf("%s: pipeline hash %s tag %d", v.Name, p.Hash, p.Tag)
		}
		if hex.EncodeToString(v.Code) != p.Wrapped || p.WrappedSize != v.Size() {
			t.Errorf("%s: wrapped bytes differ from the registry's code", v.Name)
		}
	}
}

// The flat phase is what the CBOR byte string wraps, and the UPLC text
// parses back to the same flat bytes.
func TestPipelinePhasesAgree(t *testing.T) {
	t.Parallel()
	for _, v := range All() {
		p, err := Pipe(v)
		if err != nil {
			t.Fatalf("%s: %v", v.Name, err)
		}
		var flat []byte
		if _, err := cbor.Decode(v.Code, &flat); err != nil {
			t.Fatal(err)
		}
		if p.Flat != hex.EncodeToString(flat) || p.FlatSize != len(flat) {
			t.Errorf("%s: flat phase differs from the unwrapped code", v.Name)
		}
		if !strings.HasPrefix(p.UPLC, "(program 1.1.0") {
			t.Errorf("%s: UPLC starts %.30q", v.Name, p.UPLC)
		}
		prog, err := syn.Parse(p.UPLC)
		if err != nil {
			t.Fatalf("%s: UPLC does not parse: %v", v.Name, err)
		}
		db, err := syn.NameToDeBruijn(prog)
		if err != nil {
			t.Fatal(err)
		}
		again, err := syn.Encode(db)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(again, flat) {
			t.Errorf("%s: UPLC text does not encode back to the flat bytes", v.Name)
		}
	}
}

func TestPipelineApplied(t *testing.T) {
	t.Parallel()
	v, _ := Named("vesting")
	base, err := Pipe(v)
	if err != nil {
		t.Fatal(err)
	}
	p, err := Pipe(v, data.NewInteger(big.NewInt(3)))
	if err != nil {
		t.Fatal(err)
	}
	if p.UPLC == base.UPLC || !strings.Contains(p.UPLC, "(con data (I 3))") {
		t.Fatalf("applied UPLC lacks the argument:\n%.200s", p.UPLC)
	}
	if p.Hash == base.Hash || p.WrappedSize <= base.WrappedSize {
		t.Fatalf("applied hash %s size %d, unapplied %s %d", p.Hash, p.WrappedSize, base.Hash, base.WrappedSize)
	}
	code, _ := Apply(v, data.NewInteger(big.NewInt(3)))
	if got := common.PlutusV3Script(code).Hash().String(); got != p.Hash {
		t.Fatalf("applied hash %s, gouroboros %s", p.Hash, got)
	}
	if _, err := Pipe(v); err != nil {
		t.Fatal(err)
	}
	if _, err := Pipe(v, data.NewInteger(big.NewInt(1)), data.NewInteger(big.NewInt(1))); err == nil {
		t.Fatal("wrong arity accepted")
	}
}
