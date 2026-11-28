package agenda

import (
	"bytes"
	"slices"
	"testing"

	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
	"github.com/chrisguiney/cardano-continuum/profiles"
)

// A weight on an intent the agenda never scores is dead: the actor never
// takes that intent, and a fitted cluster's transaction mix silently turns
// into whatever the agenda can do. Every shipped profile must name only
// intents its agenda can choose.
func TestShippedWeightsChoosable(t *testing.T) {
	for _, name := range slices.Concat([]string{"m1", "m3"}, profiles.Fitted) {
		raw, err := profiles.Named(name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		ps, err := profile.ParseNamed(name+".yaml", bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range ps {
			can, err := Intents(p.Agenda)
			if err != nil {
				t.Fatalf("%s/%s: %v", name, p.Name, err)
			}
			for kind := range p.Weights {
				if !slices.Contains(can, kind) {
					t.Errorf("%s/%s: agenda %s never chooses %s (it chooses %v)", name, p.Name, p.Agenda, kind, can)
				}
			}
		}
	}
}

// views is a grid of worlds wide enough for every agenda to reach each of
// its steps: stake and pool registered or not, delegated or not, DRep or
// not, with and without open proposals, calm and bursting.
func views() []View {
	pools := []PoolInfo{{ID: 1, ROI: 0.01}, {ID: 2, ROI: 0.5}}
	var out []View
	for _, stake := range []bool{false, true} {
		for _, deleg := range []population.ActorID{0, 1} {
			for _, poolReg := range []bool{false, true} {
				base := fakeView{balance: 10_000 * ada, pools: pools, stake: stake, delegation: deleg, poolReg: poolReg}
				out = append(out, base)
				for _, drep := range []bool{false, true} {
					for _, dark := range []bool{false, true} {
						w := world{
							fakeView: base, drep: drep, dark: dark, podGone: dark, burst: 3,
							proposals: []ProposalInfo{{ID: "p#0", Expires: 5, Type: "info", Pools: true}},
							dreps:     []population.ActorID{3},
						}
						out = append(out, w, trading{world: w, pos: Position{Currency: "USD"}})
					}
				}
			}
		}
	}
	return out
}

// Intents must be exactly what each policy can score above zero: a kind
// it scores but does not declare would make the shipped-profile check pass
// a dead weight, and a declared kind it never scores would let one through.
func TestIntentsMatchScores(t *testing.T) {
	actors := []population.Actor{actor(profile.Wallet, 0), actor(profile.Pool, 0), actor(profile.DRep, 0)}
	frustrated := actor(profile.Pool, 0)
	frustrated.Frustration = 10
	actors = append(actors, frustrated)
	for name, p := range registry {
		seen := map[profile.IntentKind]bool{}
		for _, a := range actors {
			for _, v := range views() {
				for _, s := range p.Score(a, v) {
					if s.Value > 0 && s.Kind != Idle && s.Kind != GoDark && s.Kind != GoLive {
						seen[s.Kind] = true
					}
				}
			}
		}
		can, err := Intents(name)
		if err != nil {
			t.Fatal(err)
		}
		for kind := range seen {
			if !slices.Contains(can, kind) {
				t.Errorf("%s scores %s but Intents leaves it out", name, kind)
			}
		}
		for _, kind := range can {
			if !seen[kind] {
				t.Errorf("%s declares %s but never scores it", name, kind)
			}
		}
	}
}

// A script user pays, calls scripts and mints at standing scores, like a
// holder: the profile's weights, not the world, decide the mix.
func TestDappCallsScriptsAtBaseline(t *testing.T) {
	ss := mustFor(t, "dapp").Score(actor(profile.Wallet, 0), fakeView{balance: 100 * ada})
	for _, k := range []profile.IntentKind{profile.IntentSend, profile.IntentScriptCall, profile.IntentMint} {
		if v := value(ss, k); !(v > 0) {
			t.Errorf("%s scored %v, want > 0", k, v)
		}
	}
	if v := value(ss, Idle); v != 1 {
		t.Errorf("idle %v, want 1", v)
	}
	poor := mustFor(t, "dapp").Score(actor(profile.Wallet, 0), fakeView{balance: ada})
	if v := value(poor, profile.IntentScriptCall); v != 0 {
		t.Errorf("script call below its cost scored %v", v)
	}
}

func TestIntentsUnknown(t *testing.T) {
	if _, err := Intents("nope"); err == nil {
		t.Fatal("want an error for an unknown agenda")
	}
}
