package agenda

import (
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
)

// A wallet never scores an intent its purpose rules out, whatever the
// owner's motivation and the world: every agenda, every purpose, the whole
// grid of views.
func TestPurposeNeverScoresOutside(t *testing.T) {
	a := actor(profile.Wallet, 0)
	for name, pol := range registry {
		for _, purpose := range population.Purposes() {
			r := Restrict(pol, purpose)
			for _, v := range views() {
				for _, s := range r.Score(a, v) {
					if s.Value > 0 && !Allows(purpose, s.Kind) {
						t.Errorf("%s/%s scores %s at %v", name, purpose, s.Kind, s.Value)
					}

					if s.Value == 0 && !Allows(purpose, s.Kind) && !strings.Contains(s.Why, string(purpose)) {
						t.Errorf("%s/%s: a ruled-out %s says %q, not its purpose", name, purpose, s.Kind, s.Why)
					}
				}
			}
		}
	}
}

// General restricts nothing: it returns the policy itself, so a wallet
// without a humans section decides exactly as before.
func TestGeneralPurposeIsTheIdentity(t *testing.T) {
	pol := marked{7}
	if got := Restrict(pol, population.General); got != Policy(pol) {
		t.Fatalf("Restrict(general) = %#v, want the policy unwrapped", got)
	}

	if got := Restrict(pol, ""); got != Policy(pol) {
		t.Fatal("an unset purpose restricted the policy")
	}

	if Restrict(nil, population.Savings) != nil {
		t.Fatal("a nil policy grew a restriction")
	}
}

// A restriction that rules nothing out leaves the decision unchanged:
// Choose skips zero scores without drawing, so the same stream picks the
// same intent.
func TestRestrictKeepsAllowedDraws(t *testing.T) {
	a := actor(profile.Wallet, 0)
	pol, _ := For("yield")
	for _, v := range views() {
		want := Decide(a, rand.New(rand.NewPCG(1, 2)), v, pol, nil)
		got := Decide(a, rand.New(rand.NewPCG(1, 2)), v, Restrict(pol, population.Staking), nil)
		if want.Intent != got.Intent && Allows(population.Staking, want.Intent.Kind) {
			t.Fatalf("staking changed an allowed choice: %v -> %v", want.Intent, got.Intent)
		}
	}
}

// Every purpose but general allows a self-transfer and a vote delegation
// (a withdrawal needs one), and a savings wallet never trades.
func TestPurposeTable(t *testing.T) {
	for _, p := range population.Purposes() {
		if !Allows(p, profile.IntentTransfer) || !Allows(p, profile.IntentDelegateDRep) {
			t.Errorf("%s refuses transfer or delegate_drep", p)
		}
	}

	for _, k := range []profile.IntentKind{profile.IntentBuyADA, profile.IntentSellADA, profile.IntentSend} {
		if Allows(population.Savings, k) {
			t.Errorf("savings allows %s", k)
		}
	}

	if !slices.Equal(Allowed(population.General), nil) {
		t.Fatal("general lists kinds; it allows all")
	}
}

// marked is a comparable policy, so a test can tell it from a wrapper.
type marked struct{ n int }

func (marked) Score(population.Actor, View) []Score { return nil }
