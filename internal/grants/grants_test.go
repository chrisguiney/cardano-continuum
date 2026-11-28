package grants

import (
	"math/rand/v2"
	"strings"
	"testing"
)

// Recipient names are generic types, never organisations, and each
// profile name is a slug of its type.
func TestRecipientsAreGeneric(t *testing.T) {
	t.Parallel()
	var share float64
	seen := map[string]bool{}
	for _, k := range Kinds() {
		share += k.Share
		if seen[k.Profile] || !strings.HasPrefix(k.Profile, ProfilePrefix) || strings.ContainsAny(k.Profile, " /") {
			t.Errorf("profile %q of %q", k.Profile, k.Name)
		}
		seen[k.Profile] = true
	}
	if share < 0.999 || share > 1.001 {
		t.Fatalf("shares sum to %v", share)
	}
	if k, ok := ByProfile(ProfilePrefix + "governance-administrator"); !ok || k.Milestones {
		t.Fatalf("governance administrator %+v, %v: paid directly", k, ok)
	}
}

// An ask is scaled to the treasury: the same draws on a treasury ten
// times larger ask ten times as much, and an action never asks more than
// maxShare of it.
func TestDrawScalesToTheTreasury(t *testing.T) {
	t.Parallel()
	all := func(string) bool { return true }
	const small = 100_000_000_000_000
	var multi int
	for i := range 500 {
		a := Draw(rand.New(rand.NewPCG(uint64(i), 1)), small, all)
		b := Draw(rand.New(rand.NewPCG(uint64(i), 1)), 10*small, all)
		if len(a) == 0 || len(a) != len(b) {
			t.Fatalf("draw %d: %d and %d payees", i, len(a), len(b))
		}
		if len(a) > 1 {
			multi++
		}
		var total uint64
		seen := map[string]bool{}
		for j := range a {
			total += a[j].Lovelace
			if seen[a[j].Kind] {
				t.Fatalf("draw %d pays %s twice", i, a[j].Kind)
			}
			seen[a[j].Kind] = true
			if a[j].Kind != b[j].Kind || a[j].Lovelace == 0 {
				t.Fatalf("draw %d payee %d: %+v vs %+v", i, j, a[j], b[j])
			}
		}
		if total > uint64(maxShare*small) {
			t.Fatalf("draw %d asks %d of %d", i, total, uint64(small))
		}
	}
	if multi == 0 || multi > 250 {
		t.Fatalf("%d of 500 actions pay several recipients", multi)
	}
}

// Only eligible recipients are paid, and none eligible draws nothing.
func TestDrawOnlyEligible(t *testing.T) {
	t.Parallel()
	only := func(p string) bool { return p == ProfilePrefix+"tooling-developer" }
	for i := range 50 {
		for _, a := range Draw(rand.New(rand.NewPCG(uint64(i), 2)), 1e14, only) {
			if a.Kind != ProfilePrefix+"tooling-developer" {
				t.Fatalf("paid %s", a.Kind)
			}
		}
	}
	if got := Draw(rand.New(rand.NewPCG(1, 2)), 1e14, func(string) bool { return false }); len(got) != 0 {
		t.Fatalf("paid %v with nobody eligible", got)
	}
}
