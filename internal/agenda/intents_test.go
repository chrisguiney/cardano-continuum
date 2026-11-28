package agenda

import (
	"bytes"
	"slices"
	"strings"
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
						funded := w
						funded.voteDeleg = true
						out = append(out, w, trading{world: w, pos: Position{Currency: "USD"}}, awarded{world: funded, rewards: ada, calls: "draw"})
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

// positioned is a view whose actor may call locked script outputs.
type positioned struct {
	fakeView
	calls string
}

func (p positioned) Calls(population.ActorID) string { return p.calls }

// The why box says what a script call would do: unlock what the actor's
// role allows, else lock. The score stays the standing one, so the fitted
// weights still set the mix.
func TestDappWhyNamesTheCall(t *testing.T) {
	v := fakeView{balance: 100 * ada}
	plain := mustFor(t, "dapp").Score(actor(profile.Wallet, 0), v)
	held := mustFor(t, "dapp").Score(actor(profile.Wallet, 0), positioned{fakeView: v, calls: "release an escrow paid to it"})
	if value(plain, profile.IntentScriptCall) != value(held, profile.IntentScriptCall) {
		t.Fatal("a held position changed the script call's score")
	}
	if w := why(held, profile.IntentScriptCall); !strings.Contains(w, "release an escrow paid to it") {
		t.Fatalf("why %q does not name the call", w)
	}
	if w := why(positioned{fakeView: v}.scores(t), profile.IntentScriptCall); !strings.Contains(w, "locks") {
		t.Fatalf("why %q without a position does not say it locks", w)
	}
}

func (p positioned) scores(t *testing.T) []Score {
	return mustFor(t, "dapp").Score(actor(profile.Wallet, 0), p)
}

func TestIntentsUnknown(t *testing.T) {
	if _, err := Intents("nope"); err == nil {
		t.Fatal("want an error for an unknown agenda")
	}
}

// awarded is a world in which the actor holds a treasury award and a
// milestone draw is due.
type awarded struct {
	world
	rewards profile.Lovelace
	calls   string
}

func (a awarded) Rewards(population.ActorID) profile.Lovelace { return a.rewards }
func (a awarded) Calls(population.ActorID) string             { return a.calls }

// A treasury recipient registers its reward account, delegates its vote
// so it may withdraw, withdraws an award once one is paid, and draws a
// milestone when one is due; with nothing paid or due it waits.
func TestGranteeTakesItsAward(t *testing.T) {
	g := mustFor(t, "grantee")
	a := actor(profile.Wallet, 0)
	base := world{fakeView: fakeView{balance: 100 * ada}, dreps: []population.ActorID{3}}
	if v := value(g.Score(a, base), profile.IntentRegisterStake); !(v > 0) {
		t.Fatalf("unregistered recipient scores register_stake %v", v)
	}
	base.stake = true
	if ss := g.Score(a, base); !(value(ss, profile.IntentDelegateDRep) > 0) || target(ss, profile.IntentDelegateDRep) != 3 {
		t.Fatalf("registered recipient does not delegate its vote: %+v", ss)
	}
	base.voteDeleg = true
	idle := g.Score(a, awarded{world: base})
	if value(idle, profile.IntentWithdraw) != 0 || value(idle, profile.IntentScriptCall) != 0 {
		t.Fatalf("recipient with nothing paid or due acts: %+v", idle)
	}
	paid := g.Score(a, awarded{world: base, rewards: 5 * ada, calls: "draw a milestone"})
	if !(value(paid, profile.IntentWithdraw) > 0) || !(value(paid, profile.IntentScriptCall) > 0) {
		t.Fatalf("recipient with an award and a draw due waits: %+v", paid)
	}
	if !strings.Contains(why(paid, profile.IntentWithdraw), "5000000") || !strings.Contains(why(paid, profile.IntentScriptCall), "draw a milestone") {
		t.Fatalf("why box %q / %q", why(paid, profile.IntentWithdraw), why(paid, profile.IntentScriptCall))
	}
}

func target(ss []Score, k profile.IntentKind) population.ActorID {
	for _, s := range ss {
		if s.Kind == k {
			return s.Target
		}
	}
	return 0
}
