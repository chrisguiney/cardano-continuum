package cohort

import (
	"errors"
	"testing"

	"github.com/chrisguiney/cardano-continuum/internal/keys"
	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
	"github.com/chrisguiney/cardano-continuum/internal/rng"
)

func TestGenesisPoolsShort(t *testing.T) {
	root, err := keys.New(rng.Seed{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	groups := []Group{{Kind: profile.Wallet, Count: 3}, {Kind: profile.Pool, Count: 1}}
	_, err = GenesisPools(2, groups, root)
	var short ShortError
	if !errors.As(err, &short) || short.Want != 2 || short.Have != 1 {
		t.Fatalf("got %v, want ShortError{2, 1}", err)
	}
	got, err := GenesisPools(1, groups, root)
	if err != nil || len(got) != 1 || got[0].Actor != 4 {
		t.Fatalf("got %+v, %v; want the pool actor 4", got, err)
	}
}

func TestRememberKeepsMostRecent(t *testing.T) {
	var st Actor
	for i := range MaxPaid + 5 {
		st.Remember(population.ActorID(i + 1))
	}
	st.Remember(7)
	if len(st.Paid) != MaxPaid {
		t.Fatalf("remembers %d payees, want %d", len(st.Paid), MaxPaid)
	}
	if last := st.Paid[len(st.Paid)-1]; last != 7 {
		t.Errorf("most recent payee %d, want 7", last)
	}
}
