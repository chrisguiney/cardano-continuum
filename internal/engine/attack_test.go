package engine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/gouroboros/ledger/conway"

	"github.com/chrisguiney/cardano-continuum/internal/adversary"
	"github.com/chrisguiney/cardano-continuum/internal/build"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/fakechain"
	"github.com/chrisguiney/cardano-continuum/internal/oracle"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

// The attack population: busy traders who pay each other and lock and
// unlock at the engine's shared script, the target of contention and
// front-running.
const traderYAML = `version: 1
profiles:
  - name: trader
    kind: wallet
    agenda: opportunist
    traits:
      competence: beta(8, 2)
      patience: const(5)
      learning_rate: const(0.05)
      care: beta(2, 2)
    budget: 1000000000
    rates:
      base:
        send: 0.05
        script_call: 0.1
`

const (
	attackWallets            = 12
	attackUntil   sched.Time = 240_000
	devnetMagic              = 42
)

// attackConfig is testConfig's chain with traders and one adversary per
// attack in ns, cleared for the fake devnet. ceiling, when positive,
// replaces the adversaries' cost ceiling.
func attackConfig(t testing.TB, seed uint64, ceiling uint64, ns ...adversary.Name) Config {
	t.Helper()
	ps, err := profile.Parse(strings.NewReader(traderYAML))
	if err != nil {
		t.Fatal(err)
	}
	advs, err := adversary.Profiles(adversary.Names()...)
	if err != nil {
		t.Fatal(err)
	}
	for i := range advs {
		if ceiling > 0 {
			advs[i].Adversary.CostCeiling = profile.Lovelace(ceiling)
		}
	}
	reg, err := profile.NewRegistry(append(ps, advs...))
	if err != nil {
		t.Fatal(err)
	}
	cfg := testConfig(t, seed, 0)
	pp := fakechain.DefaultParams()
	cfg.Build = build.ParamsFromConway(&pp, 0)
	cfg.Build.SystemStart, cfg.Build.SlotLength = time.Unix(0, 0).UTC(), time.Second
	cfg.Profiles = reg
	cfg.Cohorts = []Cohort{{Profile: "trader", Count: attackWallets}}
	for _, n := range ns {
		cfg.Cohorts = append(cfg.Cohorts, Cohort{Profile: adversary.ProfileName(n), Count: 1})
	}
	g := FakeGenesis(&cfg, devnetMagic)
	if cfg.Adversaries, err = adversary.Clear(g, g); err != nil {
		t.Fatal(err)
	}
	return cfg
}

// seven caches the seed-7 runs several tests read, by attack ("" is the
// control run): a run under the race detector takes seconds.
// seven caches the seed-7 runs. Each adversary has its own once, so the
// runs of different adversaries proceed in parallel while a run needed by
// several tests is made exactly once.
var seven = struct {
	sync.Mutex
	runs map[adversary.Name]*sevenRun
}{runs: map[adversary.Name]*sevenRun{}}

type sevenRun struct {
	once sync.Once
	w    *world
	m    adversary.Metrics
}

// sevenOf is the seed-7 run of the traders and one n adversary, or none.
func sevenOf(t *testing.T, n adversary.Name) (*world, adversary.Metrics) {
	t.Helper()
	seven.Lock()
	r, ok := seven.runs[n]
	if !ok {
		r = &sevenRun{}
		seven.runs[n] = r
	}
	seven.Unlock()
	r.once.Do(func() {
		var ns []adversary.Name
		if n != "" {
			ns = append(ns, n)
		}
		r.w, r.m = attackRun(t, attackConfig(t, 7, 0, ns...))
	})
	if r.w == nil {
		t.Fatalf("the seed-7 run of %q failed in another test", n)
	}
	return r.w, r.m
}

// attackRun runs cfg to attackUntil and returns the world and its metrics.
func attackRun(t *testing.T, cfg Config) (*world, adversary.Metrics) {
	t.Helper()
	w := newWorld(t, cfg)
	w.mem = &memStore{log: w.log}
	w.run(attackUntil, 0, nil)
	m, err := Measure(context.Background(), w.log)
	if err != nil {
		t.Fatal(err)
	}
	return w, m
}

func attacks(t testing.TB, w *world) []Attack {
	t.Helper()
	var out []Attack
	for _, r := range logRecords(t, w.log) {
		if r.Kind != KindAttack {
			continue
		}
		var a Attack
		if err := eventlog.Decode(r.Body, &a); err != nil {
			t.Fatal(err)
		}
		out = append(out, a)
	}
	return out
}

func TestGuardBlocksStart(t *testing.T) {
	t.Parallel()
	cfg := attackConfig(t, 1, 0, adversary.DustFlood)
	cfg.Adversaries = adversary.Clearance{}
	if _, err := New(cfg); !errors.Is(err, adversary.ErrNotDevnet) {
		t.Fatalf("an adversary ran without a devnet clearance: %v", err)
	}
	if _, err := Restore(cfg, nil); !errors.Is(err, adversary.ErrNotDevnet) && err == nil {
		t.Fatal("restore ran an adversary without a clearance")
	}
	// Adversary profiles nobody is spawned from need no clearance.
	quiet := attackConfig(t, 1, 0)
	quiet.Adversaries = adversary.Clearance{}
	if _, err := New(quiet); err != nil {
		t.Fatal(err)
	}
}

// TestAttackImpact runs each adversary against a control run with the same
// seed and no adversaries, and requires the impact it is there to cause.
func TestAttackImpact(t *testing.T) {
	t.Parallel()
	_, control := sevenOf(t, "")
	if control.HonestIntents == 0 || control.AttackSteps != 0 || control.Fatal != 0 {
		t.Fatalf("control run: %+v", control)
	}
	cases := []struct {
		name   adversary.Name
		impact func(c, a adversary.Metrics) bool
	}{
		{adversary.DustFlood, func(c, a adversary.Metrics) bool { return a.UtxoGrowth >= c.UtxoGrowth+50 }},
		{adversary.Contention, func(c, a adversary.Metrics) bool { return a.Grabbed > c.Grabbed }},
		{adversary.Malformed, func(c, a adversary.Metrics) bool {
			return a.Rejections > c.Rejections && a.AttackDetected == a.AttackSteps
		}},
		{adversary.FrontRun, func(c, a adversary.Metrics) bool {
			return a.HonestFailed+a.Retries > c.HonestFailed+c.Retries
		}},
		{adversary.DoubleSpend, func(c, a adversary.Metrics) bool { return a.Rejections > c.Rejections }},
	}
	for _, tc := range cases {
		t.Run(string(tc.name), func(t *testing.T) {
			t.Parallel()
			w, m := sevenOf(t, tc.name)
			if m.AttackSteps == 0 {
				t.Fatalf("no attack made: %+v", m)
			}
			if !tc.impact(control, m) {
				t.Fatalf("no impact\n%s", adversary.Table(adversary.Compare(control, m)))
			}
			t.Logf("impact\n%s", adversary.Table(adversary.Compare(control, m)))
			// Against a correct node an attack is impact, never a
			// finding: honest spends it races are judged "inputs spent".
			if m.Findings != 0 {
				t.Fatalf("%d findings (%d fatal) against a correct node", m.Findings, m.Fatal)
			}
			if w.chain.UnspentLovelace()+w.chain.Fees()+w.chain.Deposits() != genesisTotal(t, w.cfg) {
				t.Fatal("the chain lost value")
			}
		})
	}
}

func TestDeterminismAttacks(t *testing.T) {
	t.Parallel()
	cfg := attackConfig(t, 9, 0, adversary.Names()...)
	var logs [][]byte
	for range 2 {
		w := newWorld(t, cfg)
		w.run(attackUntil/2, 0, nil)
		logs = append(logs, w.logBytes())
		requireEachAttack(t, w, adversary.Names()[:5]...)
		diff, err := (&eventlog.Replayer{New: cfg.Factory(), Log: w.log}).Run(context.Background())
		if err != nil || len(diff) > 0 {
			t.Fatalf("replay of an attack run diverged: %v %v", err, diff)
		}
	}
	if !bytes.Equal(logs[0], logs[1]) {
		t.Fatal("two attack runs with one seed wrote different logs")
	}
}

// requireEachAttack fails unless every attack in ns made a step in w.
func requireEachAttack(t testing.TB, w *world, ns ...adversary.Name) {
	t.Helper()
	made := map[string]bool{}
	for _, a := range attacks(t, w) {
		made[a.Name] = true
	}
	for _, n := range ns {
		if !made[string(n)] {
			t.Fatalf("no %s step made: the run does not exercise it", n)
		}
	}
}

// TestDeterminismAttackCrash kills an attack run at sampled crash-hook
// calls, so the adversaries' cost accounts and contested outputs go
// through the snapshot. A crash between logging a send and making it
// resends on recovery, which the uninterrupted run does not log, so the
// logs are not byte-equal; each recovered run must replay its own log
// exactly, pay no intent twice and file no finding.
func TestDeterminismAttackCrash(t *testing.T) {
	t.Parallel()
	const until = attackUntil / 4
	cfg := attackConfig(t, 9, 0, adversary.Names()...)
	ref := newWorld(t, cfg)
	ref.mem = &memStore{log: ref.log}
	hooks := 0
	ref.run(until, 15, func(int) bool { hooks++; return false })
	requireEachAttack(t, ref, adversary.Names()[:5]...)
	stride := max(hooks*crashStride/24, 1)
	for at := 1; at <= hooks; at += stride {
		t.Run(fmt.Sprintf("hook%d", at), func(t *testing.T) {
			t.Parallel()
			w := newWorld(t, cfg)
			w.mem = &memStore{log: w.log}
			if c := w.run(until, 15, func(n int) bool { return n == at }); c != 1 {
				t.Fatalf("crash at %d: %d crashes", at, c)
			}
			w.finalSnapshot()
			tally, err := AuditLog(context.Background(), w.log)
			if err != nil {
				t.Fatal(err)
			}
			if tally.DoublePaid != 0 || tally.Findings != 0 {
				t.Fatalf("crash at hook %d of %d: %d double paid, %d findings", at, hooks, tally.DoublePaid, tally.Findings)
			}
		})
	}
}

// TestDeterminismGapAttack runs the exunits-block-gap adversary on its
// genesis twice with one seed and replays the run.
func TestDeterminismGapAttack(t *testing.T) {
	t.Parallel()
	cfg := gapConfig(t, 9)
	var logs [][]byte
	for range 2 {
		w := newWorld(t, cfg)
		w.run(attackUntil/2, 0, nil)
		logs = append(logs, w.logBytes())
		requireEachAttack(t, w, adversary.ExUnitsGap)
		diff, err := (&eventlog.Replayer{New: cfg.Factory(), Log: w.log}).Run(context.Background())
		if err != nil || len(diff) > 0 {
			t.Fatalf("replay of a gap run diverged: %v %v", err, diff)
		}
	}
	if !bytes.Equal(logs[0], logs[1]) {
		t.Fatal("two gap runs with one seed wrote different logs")
	}
}

func TestCostCeilingRespected(t *testing.T) {
	t.Parallel()
	const ceiling = 40_000_000
	w, _ := attackRun(t, attackConfig(t, 7, ceiling, adversary.DustFlood, adversary.DoubleSpend))
	spent := map[[2]uint64]uint64{}
	for _, r := range logRecords(t, w.log) {
		if r.Kind == KindAttack {
			var a Attack
			if err := eventlog.Decode(r.Body, &a); err != nil {
				t.Fatal(err)
			}
			k := [2]uint64{uint64(r.Actor), w.cfg.Slots.EpochAt(r.At)}
			spent[k] += a.Cost
		}
	}
	if len(spent) == 0 {
		t.Fatal("no attack made")
	}
	for k, s := range spent {
		if s > ceiling {
			t.Fatalf("actor %d spent %d in epoch %d, ceiling %d", k[0], s, k[1], ceiling)
		}
	}
	idle := 0
	for _, r := range logRecords(t, w.log) {
		if r.Kind == KindSkipped && bytes.Contains(r.Body, []byte("cost ceiling")) {
			idle++
		}
	}
	scored := scoredOff(t, w, "cost ceiling")
	if idle+scored == 0 {
		t.Fatal("the adversaries never hit their ceiling")
	}
}

// scoredOff counts adversary decisions whose scores turned an attack off
// for why.
func scoredOff(t testing.TB, w *world, why string) int {
	t.Helper()
	n := 0
	for _, r := range logRecords(t, w.log) {
		if r.Kind == KindIntentChosen && bytes.Contains(r.Body, []byte(why)) {
			n++
		}
	}
	return n
}

// lyingNode accepts every transaction it cannot decode: the fault a
// malformed-transaction adversary is there to find.
type lyingNode struct{ *FakeNode }

func (l lyingNode) Submit(ctx context.Context, tx []byte) error {
	if _, err := conway.NewConwayTransactionFromCbor(tx); err != nil {
		return nil
	}
	return l.FakeNode.Submit(ctx, tx)
}

func TestMalformedAcceptedIsFatal(t *testing.T) {
	t.Parallel()
	cfg := attackConfig(t, 7, 0, adversary.Malformed)
	w := newWorld(t, cfg)
	w.mem = &memStore{log: w.log}
	w.wrap = func(f *FakeNode) Node { return lyingNode{f} }
	w.run(attackUntil, 0, nil)
	fatal := 0
	for _, r := range logRecords(t, w.log) {
		if r.Kind != oracle.KindFinding {
			continue
		}
		f, err := oracle.Decode(r)
		if err != nil {
			t.Fatal(err)
		}
		if f.Kind == oracle.UnexpectedAccept && f.Severity == oracle.Fatal {
			fatal++
		}
	}
	if fatal == 0 {
		t.Fatal("the node accepted malformed transactions and the oracle filed nothing")
	}
}

func TestDustOutputsAtMinUtxo(t *testing.T) {
	t.Parallel()
	w, _ := sevenOf(t, adversary.DustFlood)
	cpb := w.cfg.Build.CoinsPerUtxoByte
	checked := 0
	for _, r := range logRecords(t, w.log) {
		if r.Kind != KindSubmitted {
			continue
		}
		var s Submitted
		if err := eventlog.Decode(r.Body, &s); err != nil {
			t.Fatal(err)
		}
		if s.Attack != string(adversary.DustFlood) {
			continue
		}
		tx, err := conway.NewConwayTransactionFromCbor(s.Tx)
		if err != nil {
			t.Fatal(err)
		}
		outs := tx.Outputs()
		for _, o := range outs[:len(outs)-1] {
			size, err := common.TransactionOutputCborSize(o)
			if err != nil {
				t.Fatal(err)
			}
			if want := (160 + uint64(size)) * cpb; o.Amount().Uint64() != want {
				t.Fatalf("dust output of %d lovelace, min-UTxO %d", o.Amount().Uint64(), want)
			}
			checked++
		}
	}
	if checked == 0 {
		t.Fatal("no dust output")
	}
}

func TestFrontRunOutbidsAndGoesFirst(t *testing.T) {
	t.Parallel()
	w, _ := sevenOf(t, adversary.FrontRun)
	fees := map[uint64]uint64{}
	var first []uint64
	for _, r := range logRecords(t, w.log) {
		if r.Kind == KindSubmitted {
			var s Submitted
			if err := eventlog.Decode(r.Body, &s); err != nil {
				t.Fatal(err)
			}
			if _, ok := fees[s.Intent]; !ok {
				fees[s.Intent] = s.Fee
			}
		}
		if r.Kind == eventlog.KindSubmitResult {
			var res SubmitResult
			if err := eventlog.Decode(r.Body, &res); err != nil {
				t.Fatal(err)
			}
			first = append(first, res.Intent)
		}
	}
	runs := attacks(t, w)
	if len(runs) == 0 {
		t.Fatal("no front-run")
	}
	for _, a := range runs {
		if a.Against == 0 || fees[a.Intent] <= fees[a.Against] {
			t.Fatalf("front-run %d paid %d against %d's %d", a.Intent, fees[a.Intent], a.Against, fees[a.Against])
		}
		if order(first, a.Intent) > order(first, a.Against) {
			t.Fatalf("front-run %d was sent after the honest %d", a.Intent, a.Against)
		}
	}
}

func order(xs []uint64, x uint64) int {
	for i, y := range xs {
		if y == x {
			return i
		}
	}
	return len(xs)
}

func TestDoubleSpendExactlyOne(t *testing.T) {
	t.Parallel()
	w, _ := sevenOf(t, adversary.DoubleSpend)
	races := map[uint64]bool{}
	for _, a := range attacks(t, w) {
		races[a.Intent] = true
		if len(a.Txs) < 2 {
			t.Fatalf("race %d sent %d transactions", a.Intent, len(a.Txs))
		}
	}
	answered := 0
	for _, r := range logRecords(t, w.log) {
		if r.Kind != eventlog.KindSubmitResult {
			continue
		}
		var res SubmitResult
		if err := eventlog.Decode(r.Body, &res); err != nil {
			t.Fatal(err)
		}
		if !races[res.Intent] {
			continue
		}
		answered++
		accepted := 0
		for _, v := range append([]Verdict{res.Verdict}, verdicts(res.Rivals)...) {
			if v == VerdictAccepted {
				accepted++
			}
		}
		if accepted > 1 || len(res.Rivals) == 0 {
			t.Fatalf("race %d: %d accepted of %d", res.Intent, accepted, len(res.Rivals)+1)
		}
	}
	if answered == 0 {
		t.Fatal("no race answered")
	}
}

func verdicts(as []Answer) []Verdict {
	out := make([]Verdict, len(as))
	for i, a := range as {
		out[i] = a.Verdict
	}
	return out
}
