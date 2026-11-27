package agenda

import (
	"encoding/json"
	"errors"
	"flag"
	"math"
	"math/rand/v2"
	"os"
	"slices"
	"strings"
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

// world is a fakeView with the optional capabilities: a stake-floor guard,
// pod state, governance state and a market signal. The base fakeView has
// none of them, so it exercises the fail-closed paths.
type world struct {
	fakeView
	dead    map[population.ActorID]bool // pools that do not forge
	refuse  string                      // non-empty: the floor refuses every check
	asked   *[]Intent
	dark    bool
	podGone bool

	proposals []ProposalInfo
	voted     map[ProposalID]bool
	drep      bool
	dreps     []population.ActorID
	voteDeleg bool

	burst float64
}

func ok(v fakeView) world { return world{fakeView: v} }

func (w world) Forging(p population.ActorID) bool { return !w.dead[p] }
func (w world) Allows(in Intent) (bool, string) {
	if w.asked != nil {
		*w.asked = append(*w.asked, in)
	}
	return w.refuse == "", w.refuse
}
func (w world) Dark(population.ActorID) bool                  { return w.dark }
func (w world) PodRunning(population.ActorID) bool            { return !w.podGone }
func (w world) OpenProposals() []ProposalInfo                 { return w.proposals }
func (w world) Voted(_ population.ActorID, p ProposalID) bool { return w.voted[p] }
func (w world) DRepRegistered(population.ActorID) bool        { return w.drep }
func (w world) DReps() []population.ActorID                   { return w.dreps }
func (w world) VoteDelegated(population.ActorID) bool         { return w.voteDeleg }
func (w world) Burst() float64                                { return w.burst }

func score(ss []Score, k profile.IntentKind) Score {
	for _, s := range ss {
		if s.Kind == k {
			return s
		}
	}
	return Score{Value: math.NaN()}
}

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
	for _, n := range allAgendas {
		if _, err := For(n); err != nil {
			t.Errorf("%s: %v", n, err)
		}
	}
	if _, err := For("nope"); !errors.Is(err, ErrUnknown) {
		t.Errorf("unknown: %v", err)
	}
}

var allAgendas = []profile.AgendaName{"hodl", "yield", "profit", "civic", "operator", "opportunist", "disrupt", "extract"}

// TestAdversaryStubsKeepHook: disrupt and extract score cover traffic now
// and take their attack steps from a hook the adversary package supplies.
func TestAdversaryStubsKeepHook(t *testing.T) {
	attack := descriptor{func(population.Actor, View) []Score {
		return []Score{{Kind: profile.IntentScriptCall, Value: 2, Why: "contend for a script output"}}
	}}
	for _, n := range []profile.AgendaName{"disrupt", "extract"} {
		v := fakeView{balance: 1000 * ada, stake: true}
		ss := mustFor(t, n).Score(actor(profile.Wallet, 1), v)
		active := 0
		for _, s := range ss {
			if s.Kind != Idle && s.Value > 0 {
				active++
			}
		}
		if active == 0 {
			t.Errorf("%s scores nothing but idle: %v", n, ss)
		}
		p, err := WithAttacks(n, attack)
		if err != nil {
			t.Fatal(err)
		}
		if got := value(p.Score(actor(profile.Wallet, 1), v), profile.IntentScriptCall); got != 2 {
			t.Errorf("%s with attacks: script_call %v", n, got)
		}
	}
	if _, err := WithAttacks("hodl", attack); err == nil {
		t.Fatal("attacks attached to an honest agenda")
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
		ss := p.Score(a, ok(c.v))
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
	for _, s := range mustFor(t, "yield").Score(actor(profile.Wallet, 1), ok(v)) {
		if s.Kind == profile.IntentDelegate && s.Target != 11 {
			t.Fatalf("target %d", s.Target)
		}
	}
}

func TestOperatorRegistersThenMaintains(t *testing.T) {
	p := mustFor(t, "operator")
	a := actor(profile.Pool, 1)
	unreg := p.Score(a, ok(fakeView{balance: 1000 * ada}))
	if value(unreg, profile.IntentRegisterPool) <= 0 || value(unreg, profile.IntentRetirePool) != 0 || value(unreg, GoDark) != 0 {
		t.Fatalf("unregistered: %v", unreg)
	}
	reg := p.Score(a, ok(fakeView{balance: 1000 * ada, poolReg: true}))
	if value(reg, profile.IntentRegisterPool) != 0 || value(reg, Idle) <= value(reg, GoDark) {
		t.Fatalf("registered: %v", reg)
	}
	a.Frustration = 10
	if value(p.Score(a, ok(fakeView{balance: 1000 * ada, poolReg: true})), GoDark) <= value(reg, GoDark) {
		t.Fatal("frustration should raise going dark")
	}
	gone := ok(fakeView{balance: 1000 * ada, poolReg: true})
	gone.dark, gone.podGone = true, true
	calm, upset := actor(profile.Pool, 1), a
	if value(p.Score(upset, gone), profile.IntentRetirePool) <= value(p.Score(calm, gone), profile.IntentRetirePool) {
		t.Fatal("frustration should raise retirement")
	}
	if value(p.Score(calm, gone), GoLive) <= 0 {
		t.Fatal("a dark pool can come back")
	}
}

func TestOperatorGoDarkVetoedScoresZero(t *testing.T) {
	p := mustFor(t, "operator")
	a := actor(profile.Pool, 1)
	a.Frustration = 20
	v := ok(fakeView{balance: 1000 * ada, poolReg: true})
	v.refuse = "set: forging 4 of 6 below 70%"
	if s := score(p.Score(a, v), GoDark); s.Value != 0 || !strings.Contains(s.Why, "stake floor") {
		t.Fatalf("go dark: %+v", s)
	}
	v.dark, v.podGone = true, true
	if s := score(p.Score(a, v), profile.IntentRetirePool); s.Value != 0 || !strings.Contains(s.Why, "stake floor") {
		t.Fatalf("retire: %+v", s)
	}
	// Without a guard in the view the operator never goes dark or retires.
	for _, view := range []View{fakeView{balance: 1000 * ada, poolReg: true}} {
		ss := p.Score(a, view)
		if value(ss, GoDark) != 0 || value(ss, profile.IntentRetirePool) != 0 {
			t.Fatalf("no guard: %v", ss)
		}
	}
}

func TestOperatorRetireOnlyWhenDark(t *testing.T) {
	p := mustFor(t, "operator")
	a := actor(profile.Pool, 1)
	a.Frustration = 20
	for _, c := range []struct {
		dark, gone bool
		want       bool
	}{{false, false, false}, {false, true, false}, {true, false, false}, {true, true, true}} {
		v := ok(fakeView{balance: 1000 * ada, poolReg: true})
		v.dark, v.podGone = c.dark, c.gone
		if got := value(p.Score(a, v), profile.IntentRetirePool) > 0; got != c.want {
			t.Errorf("dark %v pod gone %v: retire offered %v", c.dark, c.gone, got)
		}
	}
	// A retiring pool is not brought live again.
	v := ok(fakeView{balance: 1000 * ada, poolReg: true})
	v.dark, v.podGone = true, true
	a.State = population.Retiring
	if ss := p.Score(a, v); value(ss, GoLive) != 0 || value(ss, profile.IntentRetirePool) != 0 {
		t.Fatalf("retiring: %v", ss)
	}
}

func TestYieldMoveOntoDarkPoolVetoed(t *testing.T) {
	a := actor(profile.Wallet, 0.01)
	var asked []Intent
	v := ok(fakeView{balance: 100 * ada, stake: true, delegation: 10,
		pools: []PoolInfo{{ID: 10, ROI: 0.03}, {ID: 11, ROI: 0.05}}})
	v.dead = map[population.ActorID]bool{11: true}
	v.refuse = "projection: forging 3 of 6 below 70%"
	v.asked = &asked
	s := score(mustFor(t, "yield").Score(a, v), profile.IntentDelegate)
	if s.Value != 0 || !strings.Contains(s.Why, "stake floor") {
		t.Fatalf("move onto a dark pool: %+v", s)
	}
	if want := []Intent{{Kind: profile.IntentDelegate, Actor: 7, Target: 11}}; !slices.Equal(asked, want) {
		t.Fatalf("guard asked %v, want %v", asked, want)
	}
	// With no guard in the view, no delegation is offered at all.
	if s := score(mustFor(t, "yield").Score(a, v.fakeView), profile.IntentDelegate); s.Value != 0 {
		t.Fatalf("no guard: %+v", s)
	}
}

func TestRecoveryMoveNeverVetoed(t *testing.T) {
	a := actor(profile.Wallet, 0.01)
	v := ok(fakeView{balance: 100 * ada, stake: true, delegation: 10,
		pools: []PoolInfo{{ID: 10, ROI: 0.03}, {ID: 11, ROI: 0.05}}})
	v.dead = map[population.ActorID]bool{10: true}
	v.refuse = "set: forging 1 of 6 below 70%"
	for _, n := range []profile.AgendaName{"yield", "profit", "opportunist"} {
		s := score(mustFor(t, n).Score(a, v), profile.IntentDelegate)
		if s.Value <= 0 || s.Target != 11 {
			t.Errorf("%s: move off a dead pool: %+v", n, s)
		}
	}
}

func TestProfitWeighsGainAgainstCost(t *testing.T) {
	p := mustFor(t, "profit")
	a := actor(profile.Wallet, 0)
	pools := []PoolInfo{{ID: 10, ROI: 0.03}, {ID: 11, ROI: 0.031}}
	// 100 ADA gains 0.1 ADA per epoch from the move: less than its fee.
	small := ok(fakeView{balance: 100 * ada, stake: true, delegation: 10, pools: pools})
	if v := value(p.Score(a, small), profile.IntentDelegate); v != 0 {
		t.Fatalf("small holder moves at a loss: %v", v)
	}
	// 10,000 ADA gains 10 ADA per epoch.
	big := small
	big.balance = 10_000 * ada
	if v := value(p.Score(a, big), profile.IntentDelegate); v <= 0 {
		t.Fatalf("large holder ignores the gain: %v", v)
	}
	// A saturated pool's return is diluted, so the profit seeker stays put.
	sat := big
	sat.pools = []PoolInfo{{ID: 10, ROI: 0.03}, {ID: 11, ROI: 0.031, Stake: 1_000_000 * ada, Saturation: 100_000 * ada}}
	if v := value(p.Score(a, sat), profile.IntentDelegate); v != 0 {
		t.Fatalf("moves onto a saturated pool: %v", v)
	}
	if v := value(p.Score(a, ok(fakeView{balance: 100 * ada})), profile.IntentRegisterStake); v <= 0 {
		t.Fatal("unregistered profit seeker does not register")
	}
}

func TestCivicVotesOnOpenProposal(t *testing.T) {
	p := mustFor(t, "civic")
	drep := actor(profile.DRep, 1)
	v := ok(fakeView{balance: 1000 * ada, epoch: 10})
	v.drep = true
	v.proposals = []ProposalInfo{{ID: "aa#0", Expires: 11}, {ID: "bb#1", Expires: 20}, {ID: "cc#0", Expires: 12}}
	v.voted = map[ProposalID]bool{"cc#0": true}
	votes := map[ProposalID]float64{}
	for _, s := range p.Score(drep, v) {
		if s.Kind == profile.IntentVote && s.Value > 0 {
			votes[s.Proposal] = s.Value
		}
	}
	if len(votes) != 2 || votes["aa#0"] <= votes["bb#1"] {
		t.Fatalf("votes %v: want both unvoted proposals, the nearer expiry first", votes)
	}
	in, _ := Choose(drep.ID, rand.New(rand.NewPCG(1, 1)), []Score{{Kind: profile.IntentVote, Value: 1, Proposal: "aa#0"}}, nil)
	if in.Proposal != "aa#0" {
		t.Fatalf("intent lost its proposal: %+v", in)
	}
	v.drep = false
	if value(p.Score(drep, v), profile.IntentRegisterDRep) <= 0 {
		t.Fatal("unregistered DRep does not register")
	}
	w := ok(fakeView{balance: 1000 * ada, stake: true})
	w.dreps = []population.ActorID{30, 31}
	n := 0
	for _, s := range p.Score(actor(profile.Wallet, 1), w) {
		if s.Kind == profile.IntentDelegateDRep && s.Value > 0 {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("wallet offers %d DRep delegations, want 2", n)
	}
	if ss := p.Score(drep, fakeView{balance: 1000 * ada}); value(ss, profile.IntentVote) != 0 {
		t.Fatalf("votes with no governance view: %v", ss)
	}
}

// A registered pool votes on the open proposals stake pools may vote on and
// it has not voted on, never on one the ledger keeps from pools; a pool not
// registered, or retiring, does not vote.
func TestOperatorVotesOnlyWhereSPOsMay(t *testing.T) {
	p := mustFor(t, "operator")
	pool := actor(profile.Pool, 1)
	v := ok(fakeView{balance: 1000 * ada, epoch: 10, poolReg: true})
	v.proposals = []ProposalInfo{
		{ID: "aa#0", Expires: 11, Type: "info", Pools: true},
		{ID: "bb#0", Expires: 12, Type: "treasury_withdrawals"},
		{ID: "cc#0", Expires: 12, Type: "hard_fork_initiation", Pools: true},
		{ID: "dd#0", Expires: 12, Type: "no_confidence", Pools: true},
	}
	v.voted = map[ProposalID]bool{"dd#0": true}
	votes := map[ProposalID]float64{}
	for _, s := range p.Score(pool, v) {
		if s.Kind == profile.IntentVote && s.Value > 0 {
			votes[s.Proposal] = s.Value
		}
	}
	if len(votes) != 2 || votes["aa#0"] == 0 || votes["cc#0"] == 0 {
		t.Fatalf("votes %v: want aa#0 and cc#0 only", votes)
	}
	v.poolReg = false
	if value(p.Score(pool, v), profile.IntentVote) > 0 {
		t.Fatal("an unregistered pool votes")
	}
	v.poolReg = true
	pool.State = population.Retiring
	if value(p.Score(pool, v), profile.IntentVote) > 0 {
		t.Fatal("a retiring pool votes")
	}
}

func TestOpportunistFollowsBursts(t *testing.T) {
	p := mustFor(t, "opportunist")
	a := actor(profile.Wallet, 1)
	v := ok(fakeView{balance: 1000 * ada, stake: true})
	v.burst = 1
	calm := p.Score(a, v)
	v.burst = 4
	busy := p.Score(a, v)
	for _, k := range []profile.IntentKind{profile.IntentSend, profile.IntentScriptCall} {
		if value(busy, k) <= value(calm, k) {
			t.Errorf("%s: %v in a burst, %v calm", k, value(busy, k), value(calm, k))
		}
	}
	if value(busy, Idle) != value(calm, Idle) {
		t.Error("idle should not move with bursts")
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
	for _, n := range allAgendas {
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
	d := Decide(a, a.Stream("agenda"), ok(v), mustFor(t, "yield"), nil)
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
	d2 := Decide(a, a.Stream("agenda"), ok(v), mustFor(t, "yield"), nil)
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

// A registered pool may update its parameters, an unregistered one cannot,
// and an update rates far below idling so a hand-written operator mostly
// maintains.
func TestOperatorUpdatesRegisteredPool(t *testing.T) {
	p := mustFor(t, "operator")
	a := actor(profile.Pool, 1)
	if v := value(p.Score(a, ok(fakeView{balance: 1000 * ada})), profile.IntentUpdatePool); v != 0 {
		t.Fatalf("unregistered pool scores update %v", v)
	}
	reg := p.Score(a, ok(fakeView{balance: 1000 * ada, poolReg: true}))
	if v := value(reg, profile.IntentUpdatePool); v <= 0 || v >= value(reg, Idle) {
		t.Fatalf("registered pool scores update %v against idle %v", v, value(reg, Idle))
	}
}

// A registered DRep may retire; an unregistered one cannot.
func TestCivicDRepRetires(t *testing.T) {
	p := mustFor(t, "civic")
	drep := actor(profile.DRep, 1)
	v := ok(fakeView{balance: 1000 * ada})
	if s := value(p.Score(drep, v), profile.IntentRetireDRep); s != 0 {
		t.Fatalf("unregistered DRep scores retire %v", s)
	}
	v.drep = true
	ss := p.Score(drep, v)
	if s := value(ss, profile.IntentRetireDRep); s <= 0 || s >= value(ss, Idle) {
		t.Fatalf("registered DRep scores retire %v against idle %v", s, value(ss, Idle))
	}
}

// deposits is a world that knows the chain's deposits.
type deposits struct {
	world
	drep profile.Lovelace
}

func (d deposits) Deposit(k profile.IntentKind) (profile.Lovelace, bool) {
	if k == profile.IntentRegisterDRep {
		return d.drep, true
	}
	return 0, false
}

// A DRep funded for the chain's own deposit registers. The devnet's DRep
// deposit is 2 ADA, not mainnet's 500, and the population budget is sized
// from it: a cost table fixed at mainnet's deposit kept every such DRep
// from ever registering.
func TestCivicDRepRegistersAtChainDeposit(t *testing.T) {
	p := mustFor(t, "civic")
	drep := actor(profile.DRep, 1)
	v := deposits{world: ok(fakeView{balance: 102 * ada}), drep: 2 * ada}
	if s := score(p.Score(drep, v), profile.IntentRegisterDRep); s.Value <= 0 {
		t.Fatalf("DRep with 102 ADA does not register at a 2 ADA deposit: %+v", s)
	}
	v.balance = 2 * ada
	if s := score(p.Score(drep, v), profile.IntentRegisterDRep); s.Value != 0 {
		t.Fatalf("DRep that cannot pay the fee allowance registers: %+v", s)
	}
	if s := score(p.Score(drep, ok(fakeView{balance: 102 * ada})), profile.IntentRegisterDRep); s.Value != 0 {
		t.Fatalf("without the chain's deposits the mainnet deposit should hold: %+v", s)
	}
}
