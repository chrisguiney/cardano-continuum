package population

import (
	"testing"

	"github.com/chrisguiney/cardano-continuum/internal/profile"
)

// Without a humans section every wallet is its own owner: the human takes
// the wallet's ID, traits and competence, so Get returns what it did before
// humans existed.
func TestImplicitHumanMirrorsWallet(t *testing.T) {
	t.Parallel()
	var c Census
	w := c.Spawn(kindProfile(profile.Wallet), seed(1), 5)
	pool := c.Spawn(kindProfile(profile.Pool), seed(1), 5)

	if w.Owner != HumanID(w.ID) || w.Purpose != General {
		t.Fatalf("wallet owner %d purpose %q, want %d general", w.Owner, w.Purpose, w.ID)
	}

	if pool.Owner != 0 || pool.Purpose != "" {
		t.Fatalf("pool owner %d purpose %q, want none", pool.Owner, pool.Purpose)
	}

	h, ok := c.Human(w.Owner)
	if !ok || h.Traits != w.Traits.SampledTraits || h.Competence != w.Competence || h.Born != 5 {
		t.Fatalf("human %+v ok %v, want the wallet's traits", h, ok)
	}

	if len(h.Wallets) != 1 || h.Wallets[0] != w.ID {
		t.Fatalf("human wallets %v, want [%d]", h.Wallets, w.ID)
	}

	if _, ok := c.Human(HumanID(pool.ID)); ok {
		t.Fatal("a pool actor has a human")
	}
}

// Competence belongs to the owner: setting it through a wallet changes the
// owner's, and every wallet of that owner reads it back.
func TestCompetenceIsTheOwners(t *testing.T) {
	t.Parallel()
	var c Census
	w := c.Spawn(kindProfile(profile.Wallet), seed(2), 0)
	if err := c.SetCompetence(w.ID, 0.25); err != nil {
		t.Fatal(err)
	}

	got, _ := c.Get(w.ID)
	h, _ := c.Human(w.Owner)
	if got.Competence != 0.25 || h.Competence != 0.25 {
		t.Fatalf("competence wallet %v human %v, want 0.25", got.Competence, h.Competence)
	}
}

// Spawning the same profiles in the same order gives the same actors, owners
// included: a replay rebuilds the census exactly.
func TestImplicitSpawnIsDeterministic(t *testing.T) {
	t.Parallel()
	var a, b Census
	for range 3 {
		x := a.Spawn(kindProfile(profile.Wallet), seed(3), 1)
		y := b.Spawn(kindProfile(profile.Wallet), seed(3), 1)
		if x != y {
			t.Fatalf("spawn differs: %+v vs %+v", x, y)
		}
	}

	if n := len(a.Humans()); n != 3 {
		t.Fatalf("humans %d, want 3", n)
	}
}
