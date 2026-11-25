package agenda

import (
	"encoding/json"
	"errors"
	"flag"
	"math"
	"math/rand/v2"
	"os"
	"testing"

	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
	"github.com/chrisguiney/cardano-continuum/internal/rng"
)

var update = flag.Bool("update", false, "rewrite golden files")

const ada = profile.Lovelace(1_000_000)

type fakeView struct {
	balance    profile.Lovelace
	pools      []PoolInfo
	epoch      uint64
	stake      bool
	delegation population.ActorID
	poolReg    bool
}

func (f fakeView) Balance(population.ActorID) profile.Lovelace { return f.balance }
func (f fakeView) Pools() []PoolInfo                           { return f.pools }
func (f fakeView) Epoch() uint64                               { return f.epoch }
func (f fakeView) StakeRegistered(population.ActorID) bool     { return f.stake }
func (f fakeView) DelegatedTo(population.ActorID) (population.ActorID, bool) {
	return f.delegation, f.delegation != 0
}
func (f fakeView) PoolRegistered(population.ActorID) bool { return f.poolReg }

func actor(kind profile.Kind, patience float64) population.Actor {
	a := population.Actor{ID: 7, Kind: kind, State: population.Active, Seed: rng.Seed{1}}
	a.Traits.Patience = patience
	return a
}

func value(ss []Score, k profile.IntentKind) float64 {
	for _, s := range ss {
		if s.Kind == k {
			return s.Value
		}
	}
	return math.NaN()
}

func mustFor(t *testing.T, n profile.AgendaName) Policy {
	t.Helper()
	p, err := For(n)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestChooseProportional(t *testing.T) {
	scores := []Score{
		{Kind: profile.IntentSend, Value: 1, Why: "a"},
		{Kind: profile.IntentDelegate, Value: 2, Why: "b"},
		{Kind: Idle, Value: 1, Why: "c"},
	}
	w := map[profile.IntentKind]float64{profile.IntentDelegate: 3}
	r := rand.New(rand.NewPCG(1, 2))
	got := map[profile.IntentKind]int{}
	const n = 100_000
	for range n {
		in, _ := Choose(1, r, scores, w)
		got[in.Kind]++
	}
	// effective: send 1, delegate 6, idle 1 => 1/8, 6/8, 1/8
	for k, p := range map[profile.IntentKind]float64{profile.IntentSend: .125, profile.IntentDelegate: .75, Idle: .125} {
		if f := float64(got[k]) / n; math.Abs(f-p) > 0.01 {
			t.Errorf("%s: freq %.4f want %.3f", k, f, p)
		}
	}
}

func TestChooseAllZeroIsIdle(t *testing.T) {
	scores := []Score{{Kind: profile.IntentSend, Value: 0, Why: "x"}, {Kind: profile.IntentVote, Value: 5, Why: "y"}}
	w := map[profile.IntentKind]float64{profile.IntentVote: 0}
	in, out := Choose(3, rand.New(rand.NewPCG(1, 1)), scores, w)
	if in.Kind != Idle || in.Actor != 3 {
		t.Fatalf("got %+v", in)
	}
	if len(out) != 2 {
		t.Fatalf("scores dropped: %v", out)
	}
}

func TestChooseSortsAndDoesNotMutate(t *testing.T) {
	scores := []Score{{Kind: "z", Value: 1}, {Kind: "a", Value: 1}}
	_, out := Choose(1, rand.New(rand.NewPCG(1, 1)), scores, nil)
	if out[0].Kind != "a" || scores[0].Kind != "z" {
		t.Fatalf("out %v in %v", out, scores)
	}
}

func TestChooseDeterministicTies(t *testing.T) {
	scores := []Score{{Kind: "b", Value: 1}, {Kind: "a", Value: 1}}
	for range 50 {
		a, _ := Choose(1, rand.New(rand.NewPCG(9, 9)), scores, nil)
		b, _ := Choose(1, rand.New(rand.NewPCG(9, 9)), []Score{scores[1], scores[0]}, nil)
		if a != b {
			t.Fatalf("%v != %v", a, b)
		}
	}
}

func TestChooseTotal(t *testing.T) {
	r := rand.New(rand.NewPCG(5, 5))
	kinds := []profile.IntentKind{profile.IntentSend, profile.IntentVote, "weird", Idle}
	for range 2000 {
		var ss []Score
		present := map[profile.IntentKind]bool{Idle: true}
		for range r.IntN(6) {
			k := kinds[r.IntN(len(kinds))]
			v := []float64{0, 1, -1, math.NaN(), math.Inf(1), r.Float64()}[r.IntN(6)]
			ss = append(ss, Score{Kind: k, Value: v})
			present[k] = true
		}
		in, _ := Choose(1, r, ss, map[profile.IntentKind]float64{profile.IntentSend: math.NaN()})
		if !present[in.Kind] {
			t.Fatalf("kind %q not in %v", in.Kind, ss)
		}
	}
}

func TestForNames(t *testing.T) {
	for _, n := range []profile.AgendaName{"hodl", "yield", "operator", "disrupt", "extract"} {
		if _, err := For(n); err != nil {
			t.Errorf("%s: %v", n, err)
		}
	}
	for _, n := range []profile.AgendaName{"profit", "civic", "opportunist"} {
		if _, err := For(n); !errors.Is(err, ErrUnimplemented) {
			t.Errorf("%s: %v", n, err)
		}
	}
	if _, err := For("nope"); !errors.Is(err, ErrUnknown) {
		t.Errorf("unknown: %v", err)
	}
}

func TestStubsOnlyIdle(t *testing.T) {
	for _, n := range []profile.AgendaName{"disrupt", "extract"} {
		ss := mustFor(t, n).Score(actor(profile.Wallet, 1), fakeView{balance: 1000 * ada})
		for _, s := range ss {
			if s.Kind != Idle && s.Value != 0 {
				t.Errorf("%s scores %v", n, s)
			}
		}
		in, _ := Choose(1, rand.New(rand.NewPCG(1, 1)), ss, nil)
		if in.Kind != Idle {
			t.Errorf("%s chose %v", n, in)
		}
	}
}

func TestHodlMostlyIdle(t *testing.T) {
	p := mustFor(t, "hodl")
	a := actor(profile.Wallet, 1)
	v := fakeView{balance: 1000 * ada}
	ss := p.Score(a, v)
	r := rand.New(rand.NewPCG(3, 3))
	idle := 0
	const n = 10_000
	for range n {
		if in, _ := Choose(a.ID, r, ss, nil); in.Kind == Idle {
			idle++
		}
	}
	if f := float64(idle) / n; f < 0.9 {
		t.Fatalf("idle %.3f", f)
	}
	if value(ss, profile.IntentSend) <= 0 {
		t.Fatal("hodl should still be able to send")
	}
}

func TestYieldBehaviour(t *testing.T) {
	p := mustFor(t, "yield")
	a := actor(profile.Wallet, 0.01)
	pools := []PoolInfo{{ID: 10, ROI: 0.03}, {ID: 11, ROI: 0.05}}
	cases := []struct {
		name   string
		v      fakeView
		kind   profile.IntentKind
		target population.ActorID
		pos    bool
	}{
		{"unregistered registers", fakeView{balance: 100 * ada, pools: pools}, profile.IntentRegisterStake, 0, true},
		{"undelegated delegates to best", fakeView{balance: 100 * ada, pools: pools, stake: true}, profile.IntentDelegate, 11, true},
		{"gap above patience redelegates", fakeView{balance: 100 * ada, pools: pools, stake: true, delegation: 10}, profile.IntentDelegate, 11, true},
		{"gap below patience stays", fakeView{balance: 100 * ada, stake: true, delegation: 10,
			pools: []PoolInfo{{ID: 10, ROI: 0.045}, {ID: 11, ROI: 0.05}}}, profile.IntentDelegate, 0, false},
		{"already on best stays", fakeView{balance: 100 * ada, pools: pools, stake: true, delegation: 11}, profile.IntentDelegate, 0, false},
		{"no pools no delegate", fakeView{balance: 100 * ada, stake: true}, profile.IntentDelegate, 0, false},
	}
	for _, c := range cases {
		ss := p.Score(a, c.v)
		if got := value(ss, c.kind) > 0; got != c.pos {
			t.Errorf("%s: %v scores %v", c.name, c.kind, ss)
		}
		if c.pos {
			for _, s := range ss {
				if s.Kind == c.kind && s.Target != c.target {
					t.Errorf("%s: target %d want %d", c.name, s.Target, c.target)
				}
			}
		}
	}
}

func TestYieldTiesPickLowestPool(t *testing.T) {
	v := fakeView{balance: 100 * ada, stake: true, pools: []PoolInfo{{ID: 12, ROI: .05}, {ID: 11, ROI: .05}}}
	for _, s := range mustFor(t, "yield").Score(actor(profile.Wallet, 1), v) {
		if s.Kind == profile.IntentDelegate && s.Target != 11 {
			t.Fatalf("target %d", s.Target)
		}
	}
}

func TestOperatorRegistersThenMaintains(t *testing.T) {
	p := mustFor(t, "operator")
	a := actor(profile.Pool, 1)
	unreg := p.Score(a, fakeView{balance: 1000 * ada})
	if value(unreg, profile.IntentRegisterPool) <= 0 || value(unreg, profile.IntentRetirePool) != 0 {
		t.Fatalf("unregistered: %v", unreg)
	}
	reg := p.Score(a, fakeView{balance: 1000 * ada, poolReg: true})
	if value(reg, profile.IntentRegisterPool) != 0 || value(reg, Idle) <= value(reg, profile.IntentRetirePool) {
		t.Fatalf("registered: %v", reg)
	}
	a.Frustration = 10
	if value(p.Score(a, fakeView{balance: 1000 * ada, poolReg: true}), profile.IntentRetirePool) <= value(reg, profile.IntentRetirePool) {
		t.Fatal("frustration should raise retirement")
	}
}

func TestInfeasibleFiltered(t *testing.T) {
	poor := fakeView{balance: ada, poolReg: false}
	ss := mustFor(t, "operator").Score(actor(profile.Pool, 1), poor)
	if v := value(ss, profile.IntentRegisterPool); v != 0 {
		t.Fatalf("register_pool affordable at 1 ADA: %v", ss)
	}
	ss = mustFor(t, "hodl").Score(actor(profile.Wallet, 1), fakeView{balance: 0})
	if v := value(ss, profile.IntentSend); v != 0 {
		t.Fatalf("send with no funds: %v", ss)
	}
}

func TestScoresExplain(t *testing.T) {
	v := fakeView{balance: 1000 * ada, stake: true, delegation: 10,
		pools: []PoolInfo{{ID: 10, ROI: .03}, {ID: 11, ROI: .05}}}
	for _, n := range []profile.AgendaName{"hodl", "yield", "operator", "disrupt", "extract"} {
		for _, s := range mustFor(t, n).Score(actor(profile.Wallet, 0.01), v) {
			if s.Why == "" {
				t.Errorf("%s: empty Why for %s", n, s.Kind)
			}
		}
	}
}

func TestDecideGolden(t *testing.T) {
	v := fakeView{balance: 1000 * ada, stake: true, delegation: 10,
		pools: []PoolInfo{{ID: 10, ROI: .03}, {ID: 11, ROI: .05}}}
	a := actor(profile.Wallet, 0.01)
	d := Decide(a, a.Stream("agenda"), v, mustFor(t, "yield"), nil)
	got, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	const path = "testdata/decision.golden"
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("golden mismatch:\n%s", got)
	}
	d2 := Decide(a, a.Stream("agenda"), v, mustFor(t, "yield"), nil)
	if string(mustJSON(t, d2)) != string(mustJSON(t, d)) {
		t.Fatal("not deterministic")
	}
}

func mustJSON(t *testing.T, d Decision) []byte {
	t.Helper()
	b, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
