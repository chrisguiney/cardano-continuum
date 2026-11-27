package adversary

import (
	"errors"
	"math/rand/v2"
	"slices"
	"testing"
	"time"

	"github.com/chrisguiney/cardano-continuum/internal/agenda"
	"github.com/chrisguiney/cardano-continuum/internal/chain"
	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
)

func devnet(magic uint32) chain.Genesis {
	return chain.Genesis{Magic: magic, Start: time.Unix(0, 0).UTC(), EpochLength: 600, SecurityParam: 5, SlotLength: time.Second}
}

func TestClearRefusesAllButADevnet(t *testing.T) {
	t.Parallel()
	if (Clearance{}).Cleared() {
		t.Fatal("the zero clearance is cleared")
	}
	c, err := Clear(devnet(42), devnet(42))
	if err != nil || !c.Cleared() {
		t.Fatalf("devnet refused: %v", err)
	}
	for name, got := range map[string]chain.Genesis{
		"mainnet": devnet(764824073), "preprod": devnet(1), "preview": devnet(2), "other devnet": devnet(43),
	} {
		want := devnet(42)
		if name != "other devnet" {
			want = got
		}
		c, err := Clear(want, got)
		if !errors.Is(err, ErrNotDevnet) || c.Cleared() {
			t.Errorf("%s: cleared %t, err %v", name, c.Cleared(), err)
		}
	}
}

func TestLedgerRefusesPastTheCeiling(t *testing.T) {
	t.Parallel()
	l := NewLedger()
	l.Limit(7, 10)
	if err := l.Charge(7, 0, 6); err != nil {
		t.Fatal(err)
	}
	if err := l.Charge(7, 0, 5); !errors.Is(err, ErrCeiling) {
		t.Fatalf("charge past the ceiling: %v", err)
	}
	if got := l.Remaining(7, 0); got != 4 {
		t.Fatalf("remaining %d, want 4", got)
	}
	if err := l.Charge(7, 1, 10); err != nil {
		t.Fatalf("a new epoch starts a new allowance: %v", err)
	}
	if err := l.Charge(8, 0, 1); !errors.Is(err, ErrCeiling) {
		t.Fatalf("an actor with no ceiling spends nothing: %v", err)
	}
	back := NewLedger()
	back.Limit(7, 10)
	back.Restore(l.Entries())
	if !slices.Equal(back.Entries(), l.Entries()) || back.Remaining(7, 0) != 4 {
		t.Fatal("restore lost charges")
	}
}

func TestNewPolicyValidates(t *testing.T) {
	t.Parallel()
	if _, err := NewPolicy(profile.AdversarySpec{Attacks: []string{"dust-flood"}, CostCeiling: 1, SuccessMetric: "utxo_growth"}); err != nil {
		t.Fatal(err)
	}
	for _, s := range []profile.AdversarySpec{
		{Attacks: []string{"teleport"}, CostCeiling: 1},
		{Attacks: []string{"dust-flood"}, CostCeiling: 1, SuccessMetric: "vibes"},
	} {
		if _, err := NewPolicy(s); !errors.Is(err, ErrUnknown) {
			t.Errorf("%+v: %v", s, err)
		}
	}
}

// fakeView is the world an adversary sees in these tests.
type fakeView struct {
	bal       profile.Lovelace
	shared    []Locked
	payees    []population.ActorID
	victims   []Coin
	remaining uint64
	budgets   Budgets
}

func (v fakeView) Balance(population.ActorID) profile.Lovelace             { return v.bal }
func (fakeView) Pools() []agenda.PoolInfo                                  { return nil }
func (fakeView) Epoch() uint64                                             { return 0 }
func (fakeView) StakeRegistered(population.ActorID) bool                   { return false }
func (fakeView) DelegatedTo(population.ActorID) (population.ActorID, bool) { return 0, false }
func (fakeView) PoolRegistered(population.ActorID) bool                    { return false }
func (v fakeView) Shared() []Locked                                        { return v.shared }
func (v fakeView) Payees() []population.ActorID                            { return v.payees }
func (v fakeView) Victims() []Coin                                         { return v.victims }
func (v fakeView) Remaining(population.ActorID) uint64                     { return v.remaining }
func (v fakeView) Budgets() Budgets                                        { return v.budgets }

func scores(t *testing.T, attacks []string, v agenda.View) map[profile.IntentKind]agenda.Score {
	t.Helper()
	p, err := NewPolicy(profile.AdversarySpec{Attacks: attacks, CostCeiling: 1})
	if err != nil {
		t.Fatal(err)
	}
	out := map[profile.IntentKind]agenda.Score{}
	for _, s := range p.Score(population.Actor{ID: 1}, v) {
		out[s.Kind] = s
	}
	return out
}

func TestPolicyScoresWhatItCanAfford(t *testing.T) {
	t.Parallel()
	all := []string{"dust-flood", "utxo-contention", "malformed-cbor", "front-run", "double-spend"}
	rich := fakeView{bal: 10_000_000_000, remaining: 1_000_000_000, payees: []population.ActorID{2, 3}}
	s := scores(t, all, rich)
	if s[DustFlood.Kind()].Value <= 0 || s[Malformed.Kind()].Value <= 0 || s[DoubleSpend.Kind()].Value <= 0 {
		t.Fatalf("affordable attacks off: %+v", s)
	}
	if s[Contention.Kind()].Value != 0 {
		t.Fatalf("contention with nothing shared: %+v", s[Contention.Kind()])
	}
	if s[FrontRun.Kind()].Value != 0 {
		t.Fatalf("front-running is reactive, never chosen: %+v", s[FrontRun.Kind()])
	}
	rich.shared = []Locked{{Ref: Ref{Index: 1}, Owner: 2, Lovelace: 5_000_000}}
	if s := scores(t, all, rich); s[Contention.Kind()].Value <= 0 {
		t.Fatalf("contention off with a shared output: %+v", s[Contention.Kind()])
	}
	spent := rich
	spent.remaining = 0
	for k, sc := range scores(t, all, spent) {
		if sc.Value != 0 {
			t.Errorf("%s scored %v past the ceiling", k, sc.Value)
		}
	}
	if s := scores(t, []string{"dust-flood"}, agendaOnly{rich}); s[DustFlood.Kind()].Value != 0 {
		t.Fatal("an attack scored without an adversary view")
	}
}

// agendaOnly is a view with no adversary side: its Shared shadows the
// embedded one with another signature, so attacks fail closed.
type agendaOnly struct{ fakeView }

func (agendaOnly) Shared() {}

func TestContentionTargetsOnlySharedOutputs(t *testing.T) {
	t.Parallel()
	v := fakeView{shared: []Locked{
		{Ref: Ref{Index: 1}, Owner: 2, Lovelace: 5_000_000},
		{Ref: Ref{Index: 2}, Owner: 3, Lovelace: 5_000_000, Busy: true},
	}, payees: []population.ActorID{2, 3}}
	for i := range 50 {
		st, why := Plan(Contention, v, rand.New(rand.NewPCG(uint64(i), 1)))
		if why != "" || st.Spend == nil {
			t.Fatalf("no plan: %s", why)
		}
		if !slices.ContainsFunc(v.shared, func(l Locked) bool { return l.Ref == st.Spend.Ref }) {
			t.Fatalf("spends %+v, not a shared output", st.Spend)
		}
		if st.Spend.Busy {
			t.Fatalf("plan %d races a spend already in a mempool", i)
		}
	}
	if _, why := Plan(Contention, fakeView{}, rand.New(rand.NewPCG(1, 1))); why == "" {
		t.Fatal("planned contention with nothing shared")
	}
}

func TestFrontRunOutbids(t *testing.T) {
	t.Parallel()
	target := Locked{Ref: Ref{Index: 4}, Owner: 2, Lovelace: 5_000_000, Busy: true}
	st := Outbid(target, 300_000)
	if st.Attack != FrontRun || st.Spend == nil || st.Spend.Ref != target.Ref || st.Tip <= 300_000 {
		t.Fatalf("front-run step %+v", st)
	}
}

func TestDustAndRacePlans(t *testing.T) {
	t.Parallel()
	v := fakeView{payees: []population.ActorID{2, 3, 4, 5}}
	r := rand.New(rand.NewPCG(3, 3))
	st, why := Plan(DustFlood, v, r)
	if why != "" || len(st.Payees) < minDust || len(st.Payees) > maxDust {
		t.Fatalf("dust plan %d outputs: %s", len(st.Payees), why)
	}
	script := 0
	for _, p := range st.Payees {
		if p.Script {
			script++
		} else if !slices.Contains(v.payees, p.Actor) {
			t.Fatalf("dust to %d, not a payee", p.Actor)
		}
	}
	if script == 0 || script == len(st.Payees) {
		t.Fatalf("dust plan hits %d of %d script outputs; want a mix", script, len(st.Payees))
	}
	ds, why := Plan(DoubleSpend, v, r)
	if why != "" || len(ds.Payees) < 2 {
		t.Fatalf("double-spend plan %+v: %s", ds, why)
	}
	seen := map[population.ActorID]bool{}
	for _, p := range ds.Payees {
		if seen[p.Actor] || p.Script {
			t.Fatalf("rivals must pay distinct wallets: %+v", ds.Payees)
		}
		seen[p.Actor] = true
	}
}

func TestMalformedPlanDrawsTheWholeCorpus(t *testing.T) {
	t.Parallel()
	v := fakeView{victims: []Coin{{Ref: Ref{Index: 9}, Owner: 2, Lovelace: 50_000_000}}}
	seen := map[Mutation]bool{}
	for i := range 400 {
		st, why := Plan(Malformed, v, rand.New(rand.NewPCG(uint64(i), 7)))
		if why != "" {
			t.Fatal(why)
		}
		seen[st.Mutation] = true
		if st.Mutation == Steal && st.Victim == nil {
			t.Fatal("a steal with no victim")
		}
	}
	for _, m := range Mutations() {
		if !seen[m] {
			t.Errorf("mutation %s never drawn", m)
		}
	}
}

func TestPlanIsDeterministic(t *testing.T) {
	t.Parallel()
	v := fakeView{payees: []population.ActorID{2, 3, 4, 5}, shared: []Locked{{Ref: Ref{Index: 1}, Owner: 2}}}
	for _, n := range []Name{DustFlood, Contention, Malformed, DoubleSpend} {
		a, _ := Plan(n, v, rand.New(rand.NewPCG(5, 5)))
		b, _ := Plan(n, v, rand.New(rand.NewPCG(5, 5)))
		if a.Attack != b.Attack || a.Mutation != b.Mutation || !slices.Equal(a.Payees, b.Payees) {
			t.Fatalf("%s: one stream, two plans", n)
		}
	}
}

func TestCompare(t *testing.T) {
	t.Parallel()
	m := Metrics{HonestEnded: 10, HonestFailed: 1, UtxoGrowth: 40, Rejections: 3}
	for _, row := range Compare(m, m) {
		if row.Delta() != 0 {
			t.Fatalf("identical runs differ on %s", row.Metric)
		}
	}
	treated := m
	treated.UtxoGrowth, treated.HonestFailed = 140, 4
	got := map[string]Impact{}
	for _, row := range Compare(m, treated) {
		got[row.Metric] = row
	}
	if got["utxo_growth"].Delta() != 100 || got["honest_failure_rate"].Treated != 0.4 || got["honest_failure_rate"].Control != 0.1 {
		t.Fatalf("impact rows %+v", got)
	}
	if len(Compare(m, treated)) != len(MetricNames()) {
		t.Fatal("a metric has no impact row")
	}
}

func TestPercentile(t *testing.T) {
	t.Parallel()
	xs := []float64{5, 1, 4, 2, 3}
	if p := percentile(xs, 0.5); p != 3 {
		t.Fatalf("p50 %v", p)
	}
	if p := percentile(xs, 0.99); p != 5 {
		t.Fatalf("p99 %v", p)
	}
	if p := percentile(nil, 0.5); p != 0 {
		t.Fatalf("p50 of nothing %v", p)
	}
}

func TestProfilesParse(t *testing.T) {
	t.Parallel()
	ps, err := Profiles(Names()...)
	if err != nil {
		t.Fatal(err)
	}
	for i, p := range ps {
		if _, err := NewPolicy(*p.Adversary); err != nil || p.Agenda != "disrupt" || p.Name != ProfileName(Names()[i]) {
			t.Fatalf("%s: %+v %v", p.Name, p.Adversary, err)
		}
	}
}
