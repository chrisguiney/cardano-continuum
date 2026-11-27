package engine

import (
	"bytes"
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/blinklabs-io/gouroboros/ledger/common"

	"github.com/chrisguiney/cardano-continuum/internal/chain"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/intent"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/window"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/mistake"
	"github.com/chrisguiney/cardano-continuum/internal/oracle"
	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
	"github.com/chrisguiney/cardano-continuum/internal/submit"
)

// clumsyConfig is the honest population made careless and unskilled, with
// mistakes on, so a short run makes every applicable kind of mistake.
func clumsyConfig(t testing.TB, seed uint64) Config {
	t.Helper()
	y := strings.ReplaceAll(honestYAML, `competence: "beta(8, 2)"`, `competence: "beta(1, 4)"`)
	y = strings.ReplaceAll(y, `care: "beta(2, 2)"`, `care: "beta(1, 4)"`)
	ps, err := profile.Parse(strings.NewReader(y))
	if err != nil {
		t.Fatal(err)
	}
	reg, err := profile.NewRegistry(ps)
	if err != nil {
		t.Fatal(err)
	}
	cfg := honestConfig(t, seed)
	cfg.Profiles, cfg.Mistakes = reg, true
	return cfg
}

// findings decodes every Finding decision of a log.
func findings(t testing.TB, r eventlog.Reader) []oracle.Finding {
	t.Helper()
	var out []oracle.Finding
	for _, rec := range logRecords(t, r) {
		if rec.Kind != oracle.KindFinding {
			continue
		}
		f, err := oracle.Decode(rec)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, f)
	}
	return out
}

// mistakeClasses are the classes the catalog's mistakes cause, plus the
// "inputs spent" answers to a send that may already be at a node.
func mistakeClasses() []submit.Class {
	cs := mistake.Expect(mistake.Of(mistake.Kinds()...), true).Classes
	cs = append(cs, intent.AllAgain()...)
	return cs
}

// TestMistakesFakeRun: with mistakes on, a fake run is rejected only with
// classes the catalog causes, makes several of them, files no finding on
// the clean fake chain, and loses lovelace to wrong addresses.
func TestMistakesFakeRun(t *testing.T) {
	t.Parallel()
	cfg := clumsyConfig(t, 5)
	w := newWorld(t, cfg)
	p := cleanRun(t, w, cfg.Slots.EpochStart(3))
	tally, err := AuditLog(context.Background(), p.log)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("rejections %v, lost %d, settled %v", tally.Rejections, tally.Lost, tally.Settled)
	allowed := mistakeClasses()
	for c := range tally.Rejections {
		if !slices.Contains(allowed, submit.Class(c)) {
			t.Errorf("rejected with %s, which no mistake causes", c)
		}
	}
	if len(tally.Rejections) < 4 {
		t.Errorf("only %d rejection classes: %v", len(tally.Rejections), tally.Rejections)
	}
	for _, f := range findings(t, p.log) {
		t.Errorf("finding on the clean chain: %s %s: %s", f.Severity, f.Kind, f.Summary)
	}
	if tally.Lost == 0 {
		t.Error("no lovelace lost to a wrong address")
	}
	if s, g := p.r.Engine().Supply(), genesisTotal(t, cfg); s != g {
		t.Errorf("supply %d, genesis %d", s, g)
	}
}

// TestHonestRunFilesNoFinding: without mistakes, every answer and every
// invariant is what the oracle expects, minted tokens in the wallets
// included.
func TestHonestRunFilesNoFinding(t *testing.T) {
	t.Parallel()
	// Seed 30 spends minted tokens; seed 38 registers a stake key while
	// another registration is in flight.
	for _, seed := range []uint64{30, 38} {
		cfg := clumsyConfig(t, seed)
		cfg.Mistakes = false
		w := newWorld(t, cfg)
		p := cleanRun(t, w, cfg.Slots.EpochStart(4))
		for _, f := range findings(t, p.log) {
			t.Errorf("seed %d: %s %s: %s", seed, f.Severity, f.Kind, f.Summary)
		}
	}
}

func TestDeterminismMistakesSameSeedSameLog(t *testing.T) {
	t.Parallel()
	cfg := clumsyConfig(t, 6)
	var logs [][]byte
	for range 2 {
		w := newWorld(t, cfg)
		w.run(cfg.Slots.EpochStart(1), 25, nil)
		logs = append(logs, w.logBytes())
		if n := countKinds(t, w.log)[KindRefused]; n == 0 {
			t.Fatal("no mistake was refused: the run does not exercise the injector")
		}
		w.finalSnapshot()
	}
	if !bytes.Equal(logs[0], logs[1]) {
		t.Fatal("two runs with one seed and mistakes on wrote different logs")
	}
}

// TestDeterminismCrashWithMistakesRecovers is the crash sweep with mistakes
// on, at sampled crash points. A crash between a send and its logged answer
// makes a rejection a Resend (the bytes may have arrived), which the
// contract requires and the uninterrupted run does not log, so the logs
// are not byte-equal. Each recovered run must still replay its own log
// exactly, pay no intent twice and file no finding.
func TestDeterminismCrashWithMistakesRecovers(t *testing.T) {
	t.Parallel()
	cfg := clumsyConfig(t, 7)
	until := cfg.Slots.EpochStart(1)
	ref := newWorld(t, cfg)
	ref.mem = &memStore{log: ref.log}
	hooks := 0
	ref.run(until, 15, func(int) bool { hooks++; return false })
	if k := countKinds(t, ref.log); k[KindRefused] == 0 {
		t.Fatalf("no refusal: the run makes no mistake: %v", k)
	}
	// 40 samples, fewer under the race detector.
	stride := max(hooks*crashStride/40, 1)
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

// liar is a node whose chain-sync adds lovelace to the first output of the
// first transaction it delivers: value from nowhere, which the engine's
// accounting cannot balance.
type liar struct {
	*FakeNode
	extra uint64
}

func (l *liar) Intersect(points []eventlog.ChainPoint) (Follower, eventlog.ChainPoint, error) {
	f, at, err := l.FakeNode.Intersect(points)
	return &liarFollower{f: f, l: l}, at, err
}

type liarFollower struct {
	f Follower
	l *liar
}

func (lf *liarFollower) Next() (ChainEvent, bool, error) {
	ev, ok, err := lf.f.Next()
	if !ok || err != nil || ev.Rollback || lf.l.extra == 0 {
		return ev, ok, err
	}
	var c BlockContent
	if err := eventlog.Decode(ev.Content, &c); err != nil {
		return ev, ok, err
	}
	if len(c.Txs) == 0 || len(c.Txs[0].Outputs) == 0 {
		return ev, ok, nil
	}
	c.Txs[0].Outputs[0].Lovelace += lf.l.extra
	lf.l.extra = 0
	ev.Content, err = eventlog.Encode(c)
	return ev, ok, err
}

// TestValueFaultIsFatalFinding: a node reporting a block that creates
// lovelace makes the value monitor file one fatal finding, logged once.
func TestValueFaultIsFatalFinding(t *testing.T) {
	t.Parallel()
	cfg := testConfig(t, 21, 8)
	w := newWorld(t, cfg)
	w.wrap = func(n *FakeNode) Node { return &liar{FakeNode: n, extra: 7} }
	cleanRun(t, w, 120_000)
	fs := findings(t, w.log)
	if len(fs) != 1 || fs[0].Kind != oracle.ValueNotConserved || fs[0].Severity != oracle.Fatal {
		t.Fatalf("findings %+v, want one fatal value_not_conserved", fs)
	}
	if !slices.ContainsFunc(fs[0].Facts, func(f oracle.Fact) bool { return f.Name == "delta" && f.Value == "7" }) {
		t.Fatalf("facts %+v do not name the 7 lovelace", fs[0].Facts)
	}
	diff, err := (&eventlog.Replayer{New: cfg.Factory(), Log: w.log}).Run(context.Background())
	if err != nil || len(diff) > 0 {
		t.Fatalf("replay of a run with a finding: %v %v", diff, err)
	}
}

// offByOne answers every view query with each actor's balance one lovelace
// short at the point asked for.
type offByOne struct{ eng func() *Engine }

func (o offByOne) View(_ context.Context, at chain.Point, addrs []common.Address) (ViewAnswer, error) {
	e := o.eng()
	ans := ViewAnswer{Node: oracle.Node{Impl: "fakechain", Pool: "liar"}, At: at}
	for _, a := range addrs {
		for _, u := range e.win.View().UtxosAt(a) {
			ref := Ref{Tx: TxID(u.Id.Id()), Index: u.Id.Index()}
			ans.Utxos = append(ans.Utxos, window.Utxo(a, ref, u.Output.Amount().Uint64()-1))
		}
	}
	return ans, nil
}

// TestViewMismatchFinding: a node whose balances differ from the engine's
// at the settled point is a view_mismatch finding.
func TestViewMismatchFinding(t *testing.T) {
	t.Parallel()
	cfg := testConfig(t, 22, 4)
	w := newWorld(t, cfg)
	var p *proc
	w.monitor = offByOne{eng: func() *Engine { return p.r.Engine() }}
	var err error
	if p, err = w.start(nil, 0); err != nil {
		t.Fatal(err)
	}
	if err := p.r.Run(context.Background(), 60_000); err != nil {
		t.Fatal(err)
	}
	fs := findings(t, w.log)
	if len(fs) == 0 || !slices.ContainsFunc(fs, func(f oracle.Finding) bool { return f.Kind == oracle.ViewMismatch }) {
		t.Fatalf("findings %+v, want a view_mismatch", fs)
	}
	if n := countKinds(t, w.log)[eventlog.KindMonitorSnapshot]; n == 0 {
		t.Fatal("no MonitorSnapshot input logged")
	}
}

// forkedTips reports two pools whose chains share a block and then differ
// for more than K blocks each.
type forkedTips struct{}

func (forkedTips) Branches(context.Context) []oracle.Branch {
	pt := func(slot uint64, tag byte) chain.Point {
		p := chain.Point{Slot: slot}
		p.Hash[0] = tag
		return p
	}
	a := []chain.Point{pt(1, 0)}
	b := []chain.Point{pt(1, 0)}
	for i := uint64(2); i <= 8; i++ {
		a = append(a, pt(i, 'a'))
		b = append(b, pt(i, 'b'))
	}
	return []oracle.Branch{
		{Node: oracle.Node{Impl: "cardano-node", Pool: "p0"}, Points: a},
		{Node: oracle.Node{Impl: "dingo", Pool: "p3"}, Points: b},
	}
}

// TestLiveTipsFileDeepFork: pools whose followed chains differ beyond K
// are a fork_deeper_than_k finding.
func TestLiveTipsFileDeepFork(t *testing.T) {
	t.Parallel()
	cfg := testConfig(t, 23, 4)
	w := newWorld(t, cfg)
	w.tips = forkedTips{}
	p, err := w.start(nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.r.Run(context.Background(), 60_000); err != nil {
		t.Fatal(err)
	}
	fs := findings(t, w.log)
	if !slices.ContainsFunc(fs, func(f oracle.Finding) bool { return f.Kind == oracle.DeepFork }) {
		t.Fatalf("findings %+v, want fork_deeper_than_k", fs)
	}
}

// TestResendMatchesRestart pins why the engine does not call submit.Restart:
// a payment that was waiting for an answer when the process died is resent
// as the same bytes and its answer is read with Sent set (SubmitResult's
// Resend), which is Restart's rule 7 for the Submit state. For every answer
// the two decide the same refusal, the same next state and the same
// diagnosis; the only difference is that Restart counts the resend as a new
// send, where the engine counts the one SendTx the crash interrupted once.
func TestResendMatchesRestart(t *testing.T) {
	t.Parallel()
	pol := submit.DefaultPolicy
	a := population.Actor{Traits: population.Sampled{Patience: 10}}
	tx := chain.TxID{1}
	answers := []submit.Outcome{
		{Verdict: submit.Accepted},
		{Verdict: submit.Unreached},
		{Verdict: submit.Rejected, Failures: []submit.Failure{{Class: submit.BadInputs}}},
		{Verdict: submit.Rejected, Failures: []submit.Failure{{Class: submit.MempoolFailure}}},
		{Verdict: submit.Rejected, Failures: []submit.Failure{{Class: submit.FeeTooSmall}}},
	}
	for _, first := range []bool{true, false} {
		m := submit.Machine{State: submit.Submit, Current: tx, Attempts: 1, Sent: !first}
		for _, o := range answers {
			// The engine: the persisted machine, Sent set by Resend.
			eng := m
			eng.Sent = true
			en, ea, err := submit.Step(pol, eng, submit.Answered{Out: o}, a, nil)
			if err != nil {
				t.Fatal(err)
			}
			rm, ra, err := submit.Restart(pol, m, &tx, a, nil)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.ContainsFunc(ra, func(x submit.Action) bool { return x == submit.SendTx{Tx: tx} }) {
				t.Fatalf("Restart did not resend the same bytes: %v", ra)
			}
			rn, rna, err := submit.Step(pol, rm, submit.Answered{Out: o}, a, nil)
			if err != nil {
				t.Fatal(err)
			}
			if en.Attempts+1 != rn.Attempts {
				t.Fatalf("attempts: engine %d, restart %d", en.Attempts, rn.Attempts)
			}
			en.Attempts = rn.Attempts
			if en.State != rn.State || en.Sent != rn.Sent || !sameActions(ea, rna) {
				t.Fatalf("first=%v %v: engine %+v %v, restart %+v %v", first, o.Verdict, en, ea, rn, rna)
			}
		}
	}
}

// sameActions compares action lists ignoring backoff lengths, which depend
// on the attempt count.
func sameActions(a, b []submit.Action) bool {
	strip := func(xs []submit.Action) []submit.Action {
		return slices.DeleteFunc(slices.Clone(xs), func(x submit.Action) bool { _, ok := x.(submit.Backoff); return ok })
	}
	a, b = strip(a), strip(b)
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !sameAction(a[i], b[i]) {
			return false
		}
	}
	return true
}

func sameAction(a, b submit.Action) bool {
	da, ok1 := a.(submit.Diagnose)
	db, ok2 := b.(submit.Diagnose)
	if ok1 || ok2 {
		return ok1 && ok2 && slices.Equal(da.Failures, db.Failures)
	}
	return a == b
}
