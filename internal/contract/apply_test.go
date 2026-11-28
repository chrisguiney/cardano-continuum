package contract

import (
	"math/big"
	"testing"

	"github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/plutigo/data"
)

func TestApplySeparatesInstances(t *testing.T) {
	t.Parallel()
	v, _ := Named("vesting")
	one, err := Apply(v, data.NewInteger(big.NewInt(1)))
	if err != nil {
		t.Fatal(err)
	}
	two, err := Apply(v, data.NewInteger(big.NewInt(2)))
	if err != nil {
		t.Fatal(err)
	}
	again, err := Apply(v, data.NewInteger(big.NewInt(1)))
	if err != nil {
		t.Fatal(err)
	}
	h1, h2 := common.PlutusV3Script(one).Hash(), common.PlutusV3Script(two).Hash()
	if h1 == h2 || h1 == v.Hash {
		t.Fatalf("instances share a hash: %s %s (unapplied %s)", h1, h2, v.Hash)
	}
	if common.PlutusV3Script(again).Hash() != h1 {
		t.Fatal("applying the same argument twice gave two scripts")
	}
}

func TestApplyArity(t *testing.T) {
	t.Parallel()
	v, _ := Named("vesting")
	if _, err := Apply(v); err == nil {
		t.Fatal("applied a parameterised validator to nothing")
	}
	m, _ := Named("free_mint")
	if _, err := Apply(m, data.NewInteger(big.NewInt(1))); err == nil {
		t.Fatal("applied an unparameterised validator to an argument")
	}
	got, err := Apply(m)
	if err != nil || common.PlutusV3Script(got).Hash() != m.Hash {
		t.Fatalf("applying nothing changed free_mint: %v", err)
	}
}
