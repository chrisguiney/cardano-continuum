package engine

import (
	"bytes"
	"encoding/hex"
	"math"
	"strings"
	"testing"

	"github.com/blinklabs-io/plutigo/data"

	"github.com/chrisguiney/cardano-continuum/internal/build"
	"github.com/chrisguiney/cardano-continuum/internal/contract"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/fakechain"
	"github.com/chrisguiney/cardano-continuum/internal/market"
	"github.com/chrisguiney/cardano-continuum/internal/population"
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
	t.Parallel()
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
	if s, g := e.Summary().Supply, genesisTotal(t, cfg); s != g {
		t.Errorf("supply %d, genesis %d", s, g)
	}
	if n := e.in.Count(); n < 2 {
		t.Errorf("%d contracts deployed, want more than the first", n)
	}
	used := map[string]bool{}
	for i := range uint64(e.in.Count()) {
		if len(e.win.View().UtxosAt(build.Contract(i).Address(cfg.NetworkID))) > 0 {
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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

// Each exchange is a wallet actor of its own profile, funded at genesis with
// its float; ordinary wallets get a currency, exchanges none.
func TestMarketsExchangesAreFundedActors(t *testing.T) {
	cfg := marketsConfig(t, 1, 40)
	e, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	m := cfg.Markets
	funds, err := cfg.Genesis()
	if err != nil {
		t.Fatal(err)
	}
	for xi, x := range m.Exchanges {
		id := e.co.Market.Venue(xi)
		a, ok := e.co.Get(id)
		if !ok || a.Profile != market.ProfileName(x.Name) {
			t.Fatalf("exchange %s: actor %d profile %q", x.Name, id, a.Profile)
		}
		if f := funds[id-1]; f.Actor != uint64(id) || f.Lovelace != x.Float {
			t.Fatalf("exchange %s funded %+v, want its float %d", x.Name, f, x.Float)
		}
		if e.co.Rec(id).Trader != nil {
			t.Fatalf("exchange %s has currencies", x.Name)
		}
	}
	seen := map[int]int{}
	for _, id := range e.co.IDs() {
		if _, ok := e.co.Market.Exchange(id); ok {
			continue
		}
		tr := e.co.Rec(id).Trader
		if tr == nil {
			t.Fatalf("wallet %d has no currency", id)
		}
		seen[tr.Primary]++
	}
	if len(seen) < 3 {
		t.Fatalf("40 wallets hold only %d primary currencies", len(seen))
	}
	if h, err := cfg.fileHash(); err != nil {
		t.Fatal(err)
	} else if plain := testConfig(t, 1, 40); h == mustHash(t, &plain) {
		t.Fatal("the markets section does not reach the profile hash")
	}
}

func mustHash(t *testing.T, c *Config) [32]byte {
	t.Helper()
	h, err := c.fileHash()
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// newcomerYAML is a wallet profile that only arrives: a high birth rate
// and nothing else, so a run counts births alone.
const newcomerYAML = `version: 1
profiles:
  - name: newcomer
    kind: wallet
    agenda: hodl
    traits: {competence: "beta(9, 1)", patience: "const(5)", learning_rate: "const(0)", care: "const(1)"}
    budget: 100000000
    hazards: {birth: 2}
`

// In a market run each currency's wallets are born at the profile's rate
// times the currency's share times the market's birth factor: each
// currency's count matches the integral of that hazard over the run, and a
// birth records its currency.
func TestMarketsBirthsByCurrency(t *testing.T) {
	ps, err := profile.Parse(strings.NewReader(newcomerYAML))
	if err != nil {
		t.Fatal(err)
	}
	reg, err := profile.NewRegistry(ps)
	if err != nil {
		t.Fatal(err)
	}
	cfg := testConfig(t, 31, 0)
	cfg.Profiles, cfg.Cohorts = reg, nil
	m := profile.DefaultMarkets()
	if err := Populate(&cfg, profile.File{Markets: &m}); err != nil {
		t.Fatal(err)
	}
	const until sched.Time = 1_200_000
	w := newWorld(t, cfg)
	w.run(until, 0, nil)

	got := map[string]int{}
	for _, rec := range logRecords(t, w.log) {
		if rec.Kind != KindSpawned {
			continue
		}
		var s Spawned
		if err := eventlog.Decode(rec.Body, &s); err != nil {
			t.Fatal(err)
		}
		if s.Currency == "" {
			t.Fatalf("birth of %d records no currency", rec.Actor)
		}
		got[s.Currency]++
	}

	path := market.New(cfg.Seed, m)
	for ci, c := range m.Currencies {
		var want float64
		for at := sched.Time(0); at < until; at += 1000 {
			want += 2 * c.Share * path.Birth(ci, at)
		}
		if sd := math.Sqrt(want); math.Abs(float64(got[c.Code])-want) > 4*sd {
			t.Errorf("%s: %d births, want %.0f ± %.0f", c.Code, got[c.Code], want, 4*sd)
		}
	}
	t.Logf("births %v", got)
}

// quitterYAML is a market wallet that abandons quickly, on the market's
// synthetic hazards.
const quitterYAML = `version: 1
profiles:
  - name: quitter
    kind: wallet
    agenda: profit
    traits: {competence: "beta(9, 1)", patience: "const(5)", learning_rate: "const(0)", care: "const(1)"}
    budget: 100000000
    hazards: {abandon: 0.01, market: true}
`

// An opted-in wallet that abandons sells its ADA to an exchange first.
func TestMarketsAbandonSellsOut(t *testing.T) {
	ps, err := profile.Parse(strings.NewReader(quitterYAML))
	if err != nil {
		t.Fatal(err)
	}
	reg, err := profile.NewRegistry(ps)
	if err != nil {
		t.Fatal(err)
	}
	cfg := testConfig(t, 32, 0)
	cfg.Profiles, cfg.Cohorts = reg, []Cohort{{Profile: "quitter", Count: 30}}
	m := profile.DefaultMarkets()
	if err := Populate(&cfg, profile.File{Markets: &m}); err != nil {
		t.Fatal(err)
	}
	w := newWorld(t, cfg)
	w.run(300_000, 0, nil)

	sold := map[eventlog.ActorID]sched.Time{}
	quits := 0
	for _, rec := range logRecords(t, w.log) {
		switch rec.Kind {
		case KindSubmitted:
			var s Submitted
			if err := eventlog.Decode(rec.Body, &s); err != nil {
				t.Fatal(err)
			}
			if s.Kind == "sell_ada" {
				sold[rec.Actor] = rec.At
			}
		case KindTransition:
			var tr Transition
			if err := eventlog.Decode(rec.Body, &tr); err != nil {
				t.Fatal(err)
			}
			if tr.To != "abandoned" {
				continue
			}
			quits++
			if at, ok := sold[rec.Actor]; !ok || at != rec.At {
				t.Fatalf("actor %d abandons at %d without selling out", rec.Actor, rec.At)
			}
		}
	}
	if quits < 5 {
		t.Fatalf("only %d wallets abandoned", quits)
	}
}

// escrowCalls are the escrow releases and refunds a run submitted, each
// with the escrow datum its script run read and the actor that sent it.
type escrowCall struct {
	call  string
	actor population.ActorID
	datum contract.EscrowDatum
	ok    bool
}

func escrowCalls(t *testing.T, r eventlog.Reader) []escrowCall {
	t.Helper()
	var out []escrowCall
	for _, rec := range logRecords(t, r) {
		var s Submitted
		if rec.Kind != KindSubmitted || eventlog.Decode(rec.Body, &s) != nil || (s.Call != "release" && s.Call != "refund") {
			continue
		}
		if len(s.Scripts) != 1 {
			t.Fatalf("escrow %s with %d script runs", s.Call, len(s.Scripts))
		}
		raw, err := hex.DecodeString(s.Scripts[0].Datum)
		if err != nil {
			t.Fatal(err)
		}
		d, err := data.Decode(raw)
		if err != nil {
			t.Fatal(err)
		}
		e, ok := contract.ParseEscrowDatum(d)
		if !ok {
			t.Fatalf("escrow %s read datum %s", s.Call, s.Scripts[0].DatumJSON)
		}
		out = append(out, escrowCall{call: s.Call, actor: population.ActorID(rec.Actor), datum: e, ok: s.Scripts[0].OK})
	}
	return out
}

// Escrows are between two script users: the beneficiary claims a release
// its depositor co-signs, the depositor refunds a disputed one after its
// deadline, every script run passes, and value is conserved.
func TestEscrowTwoParties(t *testing.T) {
	t.Parallel()
	n := map[string]int{}
	// One escrow in four is disputed, so a few seeds see both outcomes.
	for seed := uint64(1); seed <= 2; seed++ {
		cfg := populationConfig(t, seed, populationFile(t, populationYAML))
		w := newWorld(t, cfg)
		p := cleanRun(t, w, 12*3600*1000)
		e := p.r.Engine()
		for _, c := range escrowCalls(t, p.log) {
			key := e.co.Wallet(c.actor).PaymentKeyHash()
			party := c.datum.Depositor
			if c.call == "release" {
				party = c.datum.Beneficiary
			}
			if !bytes.Equal(party, key[:]) || bytes.Equal(c.datum.Depositor, c.datum.Beneficiary) || !c.ok {
				t.Fatalf("seed %d: %s by actor %d of escrow %x -> %x (run ok %v)", seed, c.call, c.actor, c.datum.Depositor[:4], c.datum.Beneficiary[:4], c.ok)
			}
			n[c.call]++
		}

		if s, g := e.Summary().Supply, genesisTotal(t, cfg)+e.Summary().Inflow; s != g {
			t.Errorf("seed %d: supply %d, genesis plus inflow %d", seed, s, g)
		}
		if fs := findings(t, p.log); len(fs) > 0 {
			t.Errorf("seed %d: %d oracle findings, the first %+v", seed, len(fs), fs[0])
		}
	}
	if n["release"] == 0 || n["refund"] == 0 {
		t.Fatalf("escrow calls %v, want releases and refunds", n)
	}
	t.Logf("escrow calls %v", n)
}

// Treasury withdrawals pay the recipient types: the network proposer's
// actions name registered recipients, the votes enact some, the fake
// chain pays them from its treasury, recipients withdraw their awards (a
// milestone recipient straight into the disbursement contract) and draw
// tranches with the administrator's signature, and value is conserved
// with the awards counted as inflow.
func TestTreasuryAwards(t *testing.T) {
	t.Parallel()
	f := populationFile(t, populationYAML)
	cfg := populationConfig(t, 4, f)
	// The committee must vote for a withdrawal to be ratified.
	cfg.Committee = 3
	w := newWorld(t, cfg)
	p := cleanRun(t, w, 12*3600*1000)
	e := p.r.Engine()

	calls := map[string]int{}
	for _, rec := range logRecords(t, p.log) {
		var s Submitted
		if rec.Kind != KindSubmitted || eventlog.Decode(rec.Body, &s) != nil {
			continue
		}
		switch {
		case s.Kind == "withdraw_rewards" && s.Amount > 0:
			calls["withdraw "+cmpOr(s.Call, "direct")]++
		case s.Call == "draw":
			calls["draw"]++
		}
	}
	t.Logf("treasury %d, inflow %d, calls %v", e.win.View().Treasury(), e.Summary().Inflow, calls)
	// The view reckons the treasury from blocks; the fake chain keeps its
	// own. Payouts fall on epoch boundaries, where the two meet.
	if got, _ := w.chain.Treasury(); got != e.win.View().Treasury() {
		t.Errorf("fake treasury %d, view %d", got, e.win.View().Treasury())
	}
	if calls["withdraw award"] == 0 || calls["draw"] == 0 || e.Summary().Inflow == 0 {
		t.Fatalf("awards withdrawn %v, inflow %d", calls, e.Summary().Inflow)
	}
	if e.win.View().Treasury() >= cfg.StartTreasury() {
		t.Fatalf("treasury %d never paid out", e.win.View().Treasury())
	}

	if s, g := e.Summary().Supply, genesisTotal(t, cfg)+e.Summary().Inflow; s != g {
		t.Errorf("supply %d, genesis plus inflow %d", s, g)
	}
	if fs := findings(t, p.log); len(fs) > 0 {
		t.Errorf("%d oracle findings, the first %+v", len(fs), fs[0])
	}
}

// Script users trade at the one market: the first opens it, the others
// buy and sell, swaps built against the same market race and the losers
// are refused with BadInputs (never a failed script, so no collateral is
// forfeited) and rebuilt against the market the winner left, and value
// is conserved with the market's outputs counted as foreign.
func TestMarketContention(t *testing.T) {
	t.Parallel()
	// Busy, well-funded script users, so trades often meet at the market.
	y := strings.Replace(populationYAML, "rates: {base: {script_call: 0.05}}", "rates: {base: {script_call: 0.5}}", 1)
	y = strings.Replace(y, "budget: 1000000000\n    rates: {base: {script_call", "budget: 100000000000\n    rates: {base: {script_call", 1)
	f := populationFile(t, y)
	cfg := populationConfig(t, 4, f)
	w := newWorld(t, cfg)
	p := cleanRun(t, w, 6*3600*1000)
	e := p.r.Engine()

	trades, sent, confirmed, badInputs := map[uint64]string{}, map[uint64]int{}, map[string]int{}, 0
	for _, rec := range logRecords(t, p.log) {
		switch rec.Kind {
		case KindSubmitted:
			var s Submitted
			if eventlog.Decode(rec.Body, &s) != nil || (s.Kind != "trade" && s.Kind != "market") {
				continue
			}
			trades[s.Intent] = s.Call
			sent[s.Intent]++
			for _, r := range s.Scripts {
				if !r.OK {
					t.Fatalf("%s intent %d: a script run failed: %s", s.Call, s.Intent, r.Error)
				}
			}
		case KindRefused:
			var r Refused
			if eventlog.Decode(rec.Body, &r) == nil && trades[r.Intent] != "" && strings.Contains(r.Why, "BadInputs") {
				badInputs++
			}
		case KindConfirmed:
			var c Confirmed
			if eventlog.Decode(rec.Body, &c) == nil && trades[c.Intent] != "" {
				confirmed[trades[c.Intent]]++
			}
		}
	}

	var rebuilt int
	for id := range trades {
		if sent[id] > 1 {
			rebuilt++
		}
	}
	t.Logf("confirmed %v, refused with BadInputs %d, rebuilt %d", confirmed, badInputs, rebuilt)
	if confirmed["open"] != 1 || confirmed["buy"] == 0 || confirmed["sell"] == 0 || badInputs == 0 || rebuilt == 0 {
		t.Fatalf("confirmed %v, refused with BadInputs %d, rebuilt %d", confirmed, badInputs, rebuilt)
	}

	if s, g := e.Summary().Supply, genesisTotal(t, cfg)+e.Summary().Inflow; s != g {
		t.Errorf("supply %d, genesis plus inflow %d", s, g)
	}
	if fs := findings(t, p.log); len(fs) > 0 {
		t.Errorf("%d oracle findings, the first %+v", len(fs), fs[0])
	}
}
