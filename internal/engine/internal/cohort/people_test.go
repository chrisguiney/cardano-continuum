package cohort

import (
	"testing"

	"github.com/chrisguiney/cardano-continuum/internal/agenda"
	"github.com/chrisguiney/cardano-continuum/internal/market"
	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
	"github.com/chrisguiney/cardano-continuum/internal/rng"
)

func testPeople(spec *profile.Humans) *People {
	r := &Roster{seed: rng.Seed{3}}
	return &People{spec: spec, seed: rng.Seed{3}, proposer: "network", r: r}
}

func wallet(name string, agenda profile.AgendaName) profile.Profile {
	return profile.Profile{
		Name:   name,
		Kind:   profile.Wallet,
		Agenda: agenda,
		Traits: profile.Traits{
			Competence:   profile.Beta{A: 2, B: 2},
			Patience:     profile.Const{V: 1},
			LearningRate: profile.Const{V: 0},
			Care:         profile.Const{V: 1},
		},
	}
}

// spawn places and spawns one wallet as Life.Spawn does.
func spawn(pp *People, p profile.Profile, currency int) population.Actor {
	o, _ := pp.Place(p, currency)
	return pp.r.census.SpawnOwned(p, pp.r.seed, 0, o)
}

func TestPlaceOffWithoutHumans(t *testing.T) {
	t.Parallel()
	pp := testPeople(nil)
	if o, founded := pp.Place(wallet("w", "hodl"), -1); o != (population.Owner{}) || founded {
		t.Fatalf("Place = %+v %v, want nothing without humans", o, founded)
	}
}

// Wallets of one motivation fill each human to its target before a new
// human is founded, and each human's wallets take the motivation's purposes
// in order.
func TestPlaceFillsHumansToTarget(t *testing.T) {
	t.Parallel()
	spec := profile.DefaultHumans()
	pp := testPeople(&spec)
	var owners []population.HumanID
	for range 60 {
		owners = append(owners, spawn(pp, wallet("w", "yield"), -1).Owner)
	}

	count := map[population.HumanID]int{}
	for _, o := range owners {
		count[o]++
	}

	for _, h := range pp.list {
		if count[h.ID] > h.Target {
			t.Fatalf("human %d holds %d wallets, target %d", h.ID, count[h.ID], h.Target)
		}

		got, _ := pp.r.census.Human(h.ID)
		for i, id := range got.Wallets {
			a, _ := pp.r.census.Get(id)
			if want := purposeFor("yield", i); a.Purpose != want {
				t.Fatalf("human %d wallet %d purpose %q, want %q", h.ID, i, a.Purpose, want)
			}
		}
	}

	open := 0
	for _, h := range pp.list {
		if count[h.ID] < h.Target {
			open++
		}
	}

	if open > 1 {
		t.Fatalf("%d humans below target; a new human is founded only when none has room", open)
	}

	if len(pp.list) >= 60 {
		t.Fatalf("%d humans for 60 wallets: nobody holds two", len(pp.list))
	}
}

// A wallet never joins a human of another motivation or, when born in a
// currency, of another currency; and the actors no person owns get no
// owner.
func TestPlaceMatchesMotivationAndCurrency(t *testing.T) {
	t.Parallel()
	spec := profile.DefaultHumans()
	spec.Tiers = []profile.Tier{{Name: "only", Share: 1, Wallets: 50}}
	pp := testPeople(&spec)

	hodl := spawn(pp, wallet("a", "hodl"), 0)
	civic := spawn(pp, wallet("b", "civic"), 0)
	other := spawn(pp, wallet("c", "hodl"), 1)
	same := spawn(pp, wallet("d", "hodl"), 0)
	if civic.Owner == hodl.Owner || other.Owner == hodl.Owner || same.Owner != hodl.Owner {
		t.Fatalf("owners hodl %d civic %d other currency %d same %d", hodl.Owner, civic.Owner, other.Owner, same.Owner)
	}

	for _, p := range []profile.Profile{wallet(market.ProfileName("north"), "hodl"), wallet("network", "civic")} {
		if o, _ := pp.Place(p, -1); o.Human != 0 {
			t.Fatalf("%s got owner %d", p.Name, o.Human)
		}
	}
}

// The first wallet's purpose allows every intent its owner's motivation
// can choose, so a one-wallet human acts as the wallet did before humans.
func TestFirstPurposeAllowsTheMotivation(t *testing.T) {
	t.Parallel()
	for motivation := range purposeOrder {
		first := purposeFor(motivation, 0)
		ks, _ := agenda.Intents(motivation)
		for _, k := range ks {
			if !agenda.Allows(first, k) {
				t.Errorf("%s: first wallet (%s) refuses %s", motivation, first, k)
			}
		}
	}

	if purposeFor("operator", 0) != population.General {
		t.Fatal("an agenda without a purpose order is not general")
	}
}
