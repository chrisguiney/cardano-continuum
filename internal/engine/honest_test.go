package engine

import (
	"strings"
	"testing"

	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/fakechain"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

// honestYAML is every honest agenda: yield farmers and opportunists
// delegate to the operators' pools, citizens delegate their vote to the
// DReps, opportunists lock, unlock and mint in bursts.
const honestYAML = `version: 1
profiles:
  - name: staker
    kind: wallet
    agenda: yield
    traits:
      competence: "beta(8, 2)"
      patience: "const(0.0001)"
      learning_rate: "const(0.05)"
      care: "beta(2, 2)"
    budget: 1000000000
    rates: {base: {register_stake: 0.05, delegate: 0.05, send: 0.05}}
    hazards: {}
  - name: whale
    kind: wallet
    agenda: yield
    traits:
      competence: "beta(8, 2)"
      patience: "const(5)"
      learning_rate: "const(0.05)"
      care: "beta(2, 2)"
    budget: 200000000000
    rates: {base: {register_stake: 0.05}}
    hazards: {}
  - name: trader
    kind: wallet
    agenda: opportunist
    traits:
      competence: "beta(8, 2)"
      patience: "const(5)"
      learning_rate: "const(0.05)"
      care: "beta(2, 2)"
    budget: 1000000000
    rates: {base: {send: 0.1, script_call: 0.1, mint: 0.1, register_stake: 0.02, delegate: 0.02}}
    hazards: {}
  - name: citizen
    kind: wallet
    agenda: civic
    traits:
      competence: "beta(8, 2)"
      patience: "const(5)"
      learning_rate: "const(0.05)"
      care: "beta(2, 2)"
    budget: 1000000000
    rates: {base: {register_stake: 0.05, delegate_drep: 0.05}}
    hazards: {}
  - name: drep
    kind: drep
    agenda: civic
    traits:
      competence: "beta(8, 2)"
      patience: "const(5)"
      learning_rate: "const(0.05)"
      care: "beta(2, 2)"
    budget: 2000000000
    rates: {base: {register_drep: 0.05, vote: 0.05}}
    hazards: {}
  - name: operator
    kind: pool
    agenda: operator
    traits:
      competence: "beta(8, 2)"
      patience: "const(5)"
      learning_rate: "const(0.05)"
      care: "beta(2, 2)"
    budget: 2000000000
    rates: {base: {register_pool: 0.05, withdraw_rewards: 0.01}}
    hazards: {}
`

func honestConfig(t testing.TB, seed uint64) Config {
	t.Helper()
	ps, err := profile.Parse(strings.NewReader(honestYAML))
	if err != nil {
		t.Fatal(err)
	}
	reg, err := profile.NewRegistry(ps)
	if err != nil {
		t.Fatal(err)
	}
	slots, err := sched.NewSlots(1000, 120)
	if err != nil {
		t.Fatal(err)
	}
	pp := fakechain.DefaultParams()
	return Config{
		Seed: SeedFromInt(seed), Profiles: reg, Slots: slots, K: 3, BlockEvery: 4, Build: fullParams(), TTL: 30,
		BinaryVersion: "test", SimulatedPods: true, NOpt: uint64(pp.NOpt), GovLifetime: pp.GovActionValidityPeriod,
		GenesisPools: 3, GenesisPoolStake: 10_000_000_000,
		Committee: 3,
		Cohorts:   []Cohort{{"whale", 1}, {"operator", 4}, {"drep", 2}, {"staker", 6}, {"trader", 4}, {"citizen", 4}},
	}
}

// settledKinds counts the confirmed intents by kind.
func settledKinds(t testing.TB, r eventlog.Reader) map[string]int {
	t.Helper()
	kind := map[uint64]string{}
	out := map[string]int{}
	for _, rec := range logRecords(t, r) {
		switch rec.Kind {
		case KindSubmitted:
			var s Submitted
			if err := eventlog.Decode(rec.Body, &s); err != nil {
				t.Fatal(err)
			}
			kind[s.Intent] = cmpOr(s.Kind, "send")
		case KindConfirmed:
			var c Confirmed
			if err := eventlog.Decode(rec.Body, &c); err != nil {
				t.Fatal(err)
			}
			if k, ok := kind[c.Intent]; ok {
				out[k]++
			}
		}
	}
	return out
}

func cmpOr(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// TestHonestIntentsLand: on the fake chain every honest intent kind with a
// builder lands, an injected info proposal and committee hot keys among
// them, and the DReps vote on the proposal; value is conserved, and pools
// and DReps move along their lifecycle on confirmation.
func TestHonestIntentsLand(t *testing.T) {
	t.Parallel()
	cfg := honestConfig(t, 3)
	w := newWorld(t, cfg)
	at := cfg.Slots.EpochStart(1)
	w.controls = &scriptControls{clock: w.clock, at: []sched.Time{at, at}, cs: []control{
		{RequestID: "p", Op: "inject", Scenario: scenarioPropose}, {RequestID: "h", Op: "inject", Scenario: scenarioHotKeys},
	}}
	p := cleanRun(t, w, cfg.Slots.EpochStart(3))
	got := settledKinds(t, p.log)
	t.Logf("settled: %v", got)

	for _, k := range []string{"send", "register_stake", "delegate", "register_pool", "register_drep",
		"delegate_drep", "mint", lockKind, unlockKind, "propose", "vote", hotKind} {
		if got[k] == 0 {
			t.Errorf("no %s settled", k)
		}
	}
	e := p.r.Engine()
	if s, g := e.Supply(), genesisTotal(t, cfg); s != g {
		t.Errorf("supply %d, genesis %d", s, g)
	}
	if n := e.census.Count(profile.Pool, "active"); n != 4 {
		t.Errorf("%d active pools, want 4", n)
	}
	if n := e.census.Count(profile.DRep, "voting"); n != 2 {
		t.Errorf("%d voting DReps, want 2", n)
	}
}
