package engine

import (
	"bytes"
	"strings"
	"testing"

	"github.com/chrisguiney/cardano-continuum/internal/build"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/fakechain"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

// populationYAML is a fitted file in miniature: stakers, citizens and
// script users, plus pool, DRep and contract populations and network
// proposal rates. The per-day rates are far above a real chain's so that
// a run of a few simulated hours shows every one of them.
const populationYAML = `version: 1
profiles:
  - name: staker
    kind: wallet
    agenda: yield
    weight: 1
    traits: {competence: "beta(9, 1)", patience: "const(0.0001)", learning_rate: "const(0)", care: "const(1)"}
    budget: 1000000000
    rates: {base: {delegate: 0.01}}
    weights: {delegate: 1}
    hazards: {}
  - name: citizen
    kind: wallet
    agenda: civic
    weight: 1
    traits: {competence: "beta(9, 1)", patience: "const(5)", learning_rate: "const(0)", care: "const(1)"}
    budget: 1000000000
    rates: {base: {delegate_drep: 0.01}}
    weights: {delegate_drep: 1}
    hazards: {}
  - name: scripter
    kind: wallet
    agenda: dapp
    weight: 1
    traits: {competence: "beta(9, 1)", patience: "const(5)", learning_rate: "const(0)", care: "const(1)"}
    budget: 1000000000
    rates: {base: {script_call: 0.05}}
    weights: {script_call: 1}
    hazards: {}
populations:
  - name: pools
    kind: pool
    count: 4
    chain_count: 400
    pool:
      register_rate: 0
      retire_rate: 0
      update_rate: 40
      pledge: {dist: "lognormal(20, 1)", point: 0, point_share: 0.5}
      margin: {dist: "beta(1, 30)", point: 0, point_share: 0.2}
      cost: {dist: "lognormal(19.7, 0.3)", point: 340000000, point_share: 0.6}
  - name: dreps
    kind: drep
    count: 4
    chain_count: 400
    drep: {register_rate: 0, retire_rate: 2, vote_rate: 100, voting_share: 0.5}
  - name: contracts
    kind: contract
    chain_count: 100
    contract: {new_rate: 50, usage: "pareto(xm=1, alpha=0.5)", top_share: 0.8}
network:
  proposal_rate: {info: 30, parameter_change: 30, treasury_withdrawals: 30}
`

func populationFile(t testing.TB, y string) profile.File {
	t.Helper()
	f, err := profile.ParseFile(strings.NewReader(y))
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func populationConfig(t testing.TB, seed uint64, f profile.File) Config {
	t.Helper()
	reg, err := profile.NewRegistry(f.Profiles)
	if err != nil {
		t.Fatal(err)
	}
	slots, err := sched.NewSlots(1000, 3600)
	if err != nil {
		t.Fatal(err)
	}
	pp := fakechain.DefaultParams()
	cfg := Config{
		Seed: SeedFromInt(seed), Profiles: reg, Slots: slots, K: 3, BlockEvery: 4, Build: fullParams(), TTL: 30,
		BinaryVersion: "test", SimulatedPods: true, NOpt: uint64(pp.NOpt), GovLifetime: pp.GovActionValidityPeriod,
		Cohorts: []Cohort{{"citizen", 6}, {"scripter", 4}, {"staker", 6}},
		// Three of the four population pools are genesis pools, as a
		// devnet's are, so the stake floor has stake from the start; the
		// fourth registers itself.
		GenesisPools: 3, GenesisPoolStake: 10_000_000_000,
	}
	if err := Populate(&cfg, f); err != nil {
		t.Fatal(err)
	}
	return cfg
}

// submittedKinds counts first attempts by intent kind, and the governance
// action types proposed.
func submittedKinds(t testing.TB, r eventlog.Reader) (map[string]int, map[build.ActionType]int) {
	t.Helper()
	kinds, types, seen := map[string]int{}, map[build.ActionType]int{}, map[uint64]bool{}
	for _, rec := range logRecords(t, r) {
		if rec.Kind != KindSubmitted {
			continue
		}
		var s Submitted
		if err := eventlog.Decode(rec.Body, &s); err != nil {
			t.Fatal(err)
		}
		if seen[s.Intent] {
			continue
		}
		seen[s.Intent] = true
		kinds[cmpOr(s.Kind, "send")]++
		if s.Kind == string(profile.IntentPropose) {
			types[build.ActionType(s.Action)]++
		}
	}
	return kinds, types
}

// The engine runs a fitted file's non-wallet populations: its pool and
// DRep actors register at once, pools update and DReps vote and retire at
// the fitted rates, a network proposer submits every action type, new
// contracts are deployed and script calls spread over them, and with pools
// and DReps registered the stakers and citizens delegate.
func TestPopulationsAct(t *testing.T) {
	f := populationFile(t, populationYAML)
	cfg := populationConfig(t, 4, f)
	w := newWorld(t, cfg)
	p := cleanRun(t, w, 6*3600*1000)
	kinds, types := submittedKinds(t, p.log)
	t.Logf("submitted: %v, proposals %v", kinds, types)
	t.Logf("skipped: %v", skipReasons(t, p.log))
	for _, k := range []string{"register_pool", "update_pool", "register_drep", "vote", "retire_drep",
		"propose", "register_stake", "delegate", "delegate_drep", lockKind} {
		if kinds[k] == 0 {
			t.Errorf("no %s submitted", k)
		}
	}
	for _, a := range []build.ActionType{build.ActionInfo, build.ActionParameterChange, build.ActionTreasuryWithdrawals} {
		if types[a] == 0 {
			t.Errorf("no %s proposal", a)
		}
	}
	e := p.r.Engine()
	if s, g := e.Supply(), genesisTotal(t, cfg); s != g {
		t.Errorf("supply %d, genesis %d", s, g)
	}
	if n := e.Contracts(); n < 2 {
		t.Errorf("%d contracts deployed, want more than the first", n)
	}
	used := map[string]bool{}
	for i := range uint64(e.Contracts()) {
		if len(e.view.UtxosAt(build.Contract(i).Address(cfg.NetworkID))) > 0 {
			used[build.Contract(i).Name] = true
		}
	}
	if len(used) < 2 {
		t.Errorf("script calls reached %d contracts, want at least 2", len(used))
	}
	if fs := findings(t, p.log); len(fs) > 0 {
		t.Errorf("%d oracle findings on a clean run, the first %+v", len(fs), fs[0])
	}
}

// A fitted file's populations are in the log header's profile hash, and a
// file without populations hashes as before.
func TestProfileHashCoversPopulations(t *testing.T) {
	f := populationFile(t, populationYAML)
	h1 := headerHash(t, populationConfig(t, 1, f))
	g := populationFile(t, populationYAML)
	g.Populations[1].DRep.Vote = 99
	if h2 := headerHash(t, populationConfig(t, 1, g)); bytes.Equal(h1, h2) {
		t.Fatal("changing a DRep rate left the profile hash unchanged")
	}
	n := populationFile(t, populationYAML)
	n.Network.Proposals["info"] = 1
	if h3 := headerHash(t, populationConfig(t, 1, n)); bytes.Equal(h1, h3) {
		t.Fatal("changing a proposal rate left the profile hash unchanged")
	}

	old := testConfig(t, 1, 3)
	before, err := ProfileHash(old.Profiles)
	if err != nil {
		t.Fatal(err)
	}
	if err := Populate(&old, profile.File{Profiles: profilesOf(old.Profiles)}); err != nil {
		t.Fatal(err)
	}
	if got := headerHash(t, old); !bytes.Equal(got, before[:]) {
		t.Fatal("a file without populations changed its profile hash")
	}
}

func headerHash(t testing.TB, cfg Config) []byte {
	t.Helper()
	h, err := cfg.Header()
	if err != nil {
		t.Fatal(err)
	}
	return h.ProfileHash
}

func profilesOf(reg *profile.Registry) []profile.Profile {
	var ps []profile.Profile
	for _, n := range reg.Names() {
		p, _ := reg.Get(n)
		ps = append(ps, p)
	}
	return ps
}

// A population whose name is also a wallet profile's is refused: the
// actors' profile names would be ambiguous.
func TestPopulateRefusesNameClash(t *testing.T) {
	f := populationFile(t, strings.Replace(populationYAML, "name: dreps", "name: citizen", 1))
	cfg := testConfig(t, 1, 3)
	var err error
	if cfg.Profiles, err = profile.NewRegistry(f.Profiles); err != nil {
		t.Fatal(err)
	}
	if err := Populate(&cfg, f); err == nil {
		t.Fatal("a population named like a profile was accepted")
	}
}

// skipReasons counts Skipped and Guarded decisions by kind and reason.
func skipReasons(t testing.TB, r eventlog.Reader) map[string]int {
	t.Helper()
	out := map[string]int{}
	for _, rec := range logRecords(t, r) {
		switch rec.Kind {
		case KindSkipped:
			var s Skipped
			if err := eventlog.Decode(rec.Body, &s); err != nil {
				t.Fatal(err)
			}
			out["skipped "+s.Kind+": "+s.Why]++
		case KindGuarded:
			var g Guarded
			if err := eventlog.Decode(rec.Body, &g); err != nil {
				t.Fatal(err)
			}
			out["guarded "+g.Kind+": "+g.Why]++
		}
	}
	return out
}

// A population actor's clocks start once it is registered: the wake that
// found it registered was drawn at the boot rate, so firing a clock on it
// would give every actor one event at once. With rates of one event in
// years, a few hours see no update and no retirement.
func TestPopulationClocksStartAfterRegistering(t *testing.T) {
	y := strings.NewReplacer("update_rate: 40", "update_rate: 0.001",
		"retire_rate: 2, vote_rate: 100", "retire_rate: 0.001, vote_rate: 100").Replace(populationYAML)
	cfg := populationConfig(t, 4, populationFile(t, y))
	p := cleanRun(t, newWorld(t, cfg), 6*3600*1000)
	kinds, _ := submittedKinds(t, p.log)
	if kinds["update_pool"] != 0 || kinds["retire_drep"] != 0 {
		t.Fatalf("submitted %v: clocks fired at the boot rate", kinds)
	}
	if kinds["register_drep"] == 0 || kinds["register_pool"] == 0 {
		t.Fatalf("submitted %v: population actors did not register", kinds)
	}
}
