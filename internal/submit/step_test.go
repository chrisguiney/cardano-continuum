package submit

import (
	"context"
	"encoding/binary"
	"errors"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/blinklabs-io/gouroboros/ledger/common"

	"github.com/chrisguiney/cardano-continuum/internal/chain"
	"github.com/chrisguiney/cardano-continuum/internal/plan"
	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/store"
)

const testPatience = 3

type noOwners struct{}

func (noOwners) Owner(common.Address) (population.ActorID, bool) { return 0, false }

type noDisk struct{}

func (noDisk) Commit(context.Context, store.Blob) error { return nil }

func tid(n uint64) plan.TxID {
	var h plan.TxID
	binary.BigEndian.PutUint64(h[24:], n)
	return h
}

func pref(n uint64) plan.Ref { return plan.Ref{Tx: tid(n)} }

func testActor(patience float64) population.Actor {
	var a population.Actor
	a.ID = 1
	a.Traits.Patience = patience
	return a
}

var (
	feeOut   = Outcome{Verdict: Rejected, Failures: []Failure{{Class: FeeTooSmall}}}
	spentOut = Outcome{Verdict: Rejected, Failures: []Failure{{Class: BadInputs}}}
	mempOut  = Outcome{Verdict: Rejected, Failures: []Failure{{Class: MempoolFailure}}}
	okOut    = Outcome{Verdict: Accepted}
	lostOut  = Outcome{Verdict: Unreached, Err: errors.New("timeout")}
)

// rig runs a Machine against a real plan.Ledger. It plays the caller of
// Step: a Build action reserves through the ledger and a Refused action
// calls Ledger.Refused. Nothing else touches the ledger.
type rig struct {
	t       *testing.T
	led     *plan.Ledger
	m       Machine
	a       population.Actor
	r       *rand.Rand
	intent  plan.IntentID
	ttl     uint64
	next    uint64     // next transaction ID
	inputs  []plan.Ref // what the next Build spends
	refused []plan.TxID
	sends   []plan.TxID
	tip     chain.Point
}

func newRig(t *testing.T) *rig {
	t.Helper()
	led, err := plan.New(plan.Config{K: 2, Owners: noOwners{}, Store: noDisk{}}, []plan.Utxo{
		{Ref: pref(101), Owner: 1, Lovelace: 10_000_000},
		{Ref: pref(102), Owner: 1, Lovelace: 20_000_000},
	})
	if err != nil {
		t.Fatal(err)
	}
	return &rig{t: t, led: led, a: testActor(testPatience), r: rand.New(rand.NewPCG(1, 2)),
		intent: 1, ttl: 5, next: 1, inputs: []plan.Ref{pref(101)}}
}

// step feeds ev and then every follow-up the caller owes: building, refusing
// and diagnosing. It returns all actions in order.
func (g *rig) step(ev Event) []Action {
	g.t.Helper()
	m, acts, err := Step(DefaultPolicy, g.m, ev, g.a, g.r)
	if err != nil {
		g.t.Fatalf("Step(%T) in %v: %v", ev, g.m.State, err)
	}
	g.m = m
	all := append([]Action(nil), acts...)
	for _, act := range acts {
		switch act := act.(type) {
		case BuildAttempt:
			tx := tid(g.next)
			g.next++
			err := g.led.Reserve(context.Background(), g.intent, plan.Attempt{Tx: tx, Actor: 1, Inputs: g.inputs, TTL: g.ttl})
			if err != nil {
				all = append(all, g.step(BuildFailed{})...)
			} else {
				all = append(all, g.step(Built{Tx: tx})...)
			}
		case SendTx:
			g.sends = append(g.sends, act.Tx)
		case Refused:
			if _, err := g.led.Refused(context.Background(), act.Tx); err != nil {
				g.t.Fatal(err)
			}
			g.refused = append(g.refused, act.Tx)
		case Diagnose:
			all = append(all, g.step(Diagnosed{Class: Perfect{}.Diagnose(g.r, act.Failures)})...)
		}
	}
	return all
}

func (g *rig) start() { g.t.Helper(); g.step(Start{}) }

func (g *rig) unfinished() int { return len(g.led.Unfinished()) }

func count[T Action](acts []Action) int {
	n := 0
	for _, a := range acts {
		if _, ok := a.(T); ok {
			n++
		}
	}
	return n
}

func TestNodeRefusalReleases(t *testing.T) {
	t.Parallel()
	g := newRig(t)
	g.start()
	first := g.m.Current
	acts := g.step(Answered{Out: feeOut})
	if n := count[Refused](acts); n != 1 || len(g.refused) != 1 || g.refused[0] != first {
		t.Fatalf("Refused actions %d, ledger refusals %v, want exactly %v", n, g.refused, first)
	}
	if g.m.State != Submit || g.m.Current == first {
		t.Fatalf("after the refusal the machine is %v on %x, want a rebuilt attempt", g.m.State, g.m.Current[:4])
	}
	for _, p := range g.led.Unfinished() {
		if p.Tx == first {
			t.Fatal("the refused attempt is still unfinished")
		}
	}
}

func TestDropYieldsRebuildNotRelease(t *testing.T) {
	t.Parallel()
	g := newRig(t)
	g.start()
	g.step(Answered{Out: okOut})
	first := g.m.Current
	acts := g.step(Dropped{})
	if count[Refused](acts) != 0 || len(g.refused) != 0 {
		t.Fatal("a drop notice refused an attempt")
	}
	if count[BuildAttempt](acts) != 1 {
		t.Fatalf("a drop notice gave %d Build actions, want 1", count[BuildAttempt](acts))
	}
	if g.m.Current == first {
		t.Fatal("the rebuilt attempt reuses the dropped attempt's ID")
	}
	if g.unfinished() != 2 {
		t.Fatalf("%d unfinished attempts, want the dropped one and its rebuild", g.unfinished())
	}
}

func TestRebuildGoesThroughReserve(t *testing.T) {
	t.Parallel()
	g := newRig(t)
	g.start()
	g.step(Answered{Out: okOut})
	g.step(Dropped{}) // the same input: conflicts with the unfinished attempt
	if g.m.State != Submit {
		t.Fatalf("rebuild on a conflicting input left the machine in %v", g.m.State)
	}

	h := newRig(t)
	h.start()
	h.step(Answered{Out: okOut})
	first := h.m.Current
	h.inputs = []plan.Ref{pref(102)} // would land beside the unfinished attempt
	acts := h.step(Dropped{})
	if h.m.State != GaveUp || count[GiveUp](acts) != 1 {
		t.Fatalf("Reserve refused the rebuild; machine is %v, want GaveUp (fail closed)", h.m.State)
	}
	if count[SendTx](acts) != 0 {
		t.Fatal("a refused rebuild was sent")
	}
	if h.unfinished() != 1 || h.led.Unfinished()[0].Tx != first {
		t.Fatal("the failed rebuild changed the ledger")
	}
}

func TestRolledBackYieldsNoRelease(t *testing.T) {
	t.Parallel()
	g := newRig(t)
	g.start()
	g.step(Answered{Out: okOut})
	first := g.m.Current
	g.step(InBlock{Tx: first})
	acts := g.step(LeftView{})
	if count[Refused](acts) != 0 || count[BuildAttempt](acts) != 0 {
		t.Fatalf("a rollback released or rebuilt: %v", acts)
	}
	sends := 0
	for _, a := range acts {
		if s, ok := a.(SendTx); ok && s.Tx == first {
			sends++
		}
	}
	if sends != 1 || g.m.State != Submit || g.m.Current != first {
		t.Fatalf("rollback: %d resends of the same bytes, state %v", sends, g.m.State)
	}
	if g.unfinished() != 1 {
		t.Fatal("the rolled-back attempt is no longer unfinished")
	}
}

func TestRollbackKeepsAttempts(t *testing.T) {
	t.Parallel()
	g := newRig(t)
	g.start()
	g.step(Answered{Out: okOut})
	g.step(InBlock{Tx: g.m.Current})
	before := g.m.Attempts
	g.step(LeftView{})
	if g.m.Attempts != before+1 {
		t.Fatalf("Attempts %d -> %d across a rollback, want %d (the resend counts, nothing resets)", before, g.m.Attempts, before+1)
	}
	g.step(Answered{Out: okOut})
	g.step(InBlock{Tx: g.m.Current})
	g.step(LeftView{})
	g.step(Answered{Out: okOut})
	g.step(InBlock{Tx: g.m.Current})
	acts := g.step(LeftView{})
	if g.m.State != GaveUp || count[SendTx](acts) != 0 {
		t.Fatalf("third rollback with patience %d: state %v, sends %d", testPatience, g.m.State, count[SendTx](acts))
	}
}

func TestGaveUpWaitsForSettledExpiry(t *testing.T) {
	t.Parallel()
	g := newRig(t)
	g.start()
	g.step(Answered{Out: lostOut})
	g.step(Answered{Out: lostOut})
	g.step(Answered{Out: lostOut})
	if g.m.State != GaveUp {
		t.Fatalf("state %v after %d unanswered sends, want GaveUp", g.m.State, testPatience)
	}
	if len(g.sends) != testPatience {
		t.Fatalf("%d sends, want %d", len(g.sends), testPatience)
	}
	if len(g.refused) != 0 || g.unfinished() != 1 {
		t.Fatalf("giving up released: refused %v, unfinished %d", g.refused, g.unfinished())
	}
	// Blocks at TTL-1, TTL, TTL+1, TTL+2 with K=2 settle the block at TTL:
	// only then does the planner write the attempt off.
	var dead []plan.TxID
	for i, slot := range []uint64{g.ttl - 1, g.ttl, g.ttl + 1, g.ttl + 2} {
		res, err := g.led.Advance(context.Background(), g.forward(slot, uint64(i+1)))
		if err != nil {
			t.Fatal(err)
		}
		if i < 3 && len(res.Dead) > 0 {
			t.Fatalf("written off at block %d, before the settled slot reached the TTL", i+1)
		}
		dead = append(dead, res.Dead...)
	}
	if len(dead) != 1 || g.unfinished() != 0 {
		t.Fatalf("settled expiry wrote off %d attempts, %d unfinished", len(dead), g.unfinished())
	}
}

func (g *rig) forward(slot, n uint64) chain.Event {
	var h common.Blake2b256
	binary.BigEndian.PutUint64(h[24:], n)
	b := chain.Block{Point: chain.Point{Slot: slot, Hash: h}, Prev: g.tip.Hash}
	g.tip = b.Point
	return chain.Event{Kind: chain.Forward, Block: b, Point: b.Point}
}

func TestGaveUpThenSettledIsPaid(t *testing.T) {
	t.Parallel()
	g := newRig(t)
	g.start()
	for range testPatience {
		g.step(Answered{Out: lostOut})
	}
	acts := g.step(Settled{Tx: g.m.Current})
	if g.m.State != GaveUp || !g.m.Paid || len(acts) != 0 {
		t.Fatalf("settle after give-up: state %v paid %v actions %v, want GaveUp, paid, none", g.m.State, g.m.Paid, acts)
	}
}

func TestRestartTreatsUnfinishedAsSent(t *testing.T) {
	t.Parallel()
	g := newRig(t)
	g.start()
	first := g.m.Current
	// The process died with the first send unanswered; it may have landed.
	m, acts, err := Restart(DefaultPolicy, g.m, &first, g.a, g.r)
	if err != nil {
		t.Fatal(err)
	}
	if m.State != Submit || !m.Sent || m.Current != first {
		t.Fatalf("restart: %+v, want Submit on the same bytes, marked sent", m)
	}
	if n := count[SendTx](acts); n != 1 || acts[len(acts)-1].(SendTx).Tx != first {
		t.Fatalf("restart actions %v, want one SendTx of the same bytes", acts)
	}
	g.m = m
	acts = g.step(Answered{Out: spentOut})
	if count[Refused](acts) != 0 || len(g.refused) != 0 {
		t.Fatal("inputs spent after a restart refused the attempt")
	}
	if g.unfinished() < 1 || g.led.Unfinished()[0].Tx != first {
		t.Fatal("the possibly landed attempt left the ledger")
	}
}

func TestRestartWithoutUnfinishedBuilds(t *testing.T) {
	t.Parallel()
	m, acts, err := Restart(DefaultPolicy, Machine{State: Pending, Attempts: 1}, nil, testActor(testPatience), rand.New(rand.NewPCG(1, 2)))
	if err != nil || m.State != Build || count[BuildAttempt](acts) != 1 {
		t.Fatalf("restart with nothing unfinished: %v %v %v", m.State, acts, err)
	}
	for _, s := range []State{Done, GaveUp} {
		m, acts, err = Restart(DefaultPolicy, Machine{State: s}, nil, testActor(testPatience), rand.New(rand.NewPCG(1, 2)))
		if err != nil || m.State != s || len(acts) != 0 {
			t.Fatalf("restart of %v: %v %v %v", s, m.State, acts, err)
		}
	}
}

func TestUnreachedIsNotRefusal(t *testing.T) {
	t.Parallel()
	g := newRig(t)
	g.start()
	first := g.m.Current
	acts := g.step(Answered{Out: lostOut})
	if count[Refused](acts) != 0 || len(g.refused) != 0 {
		t.Fatal("no answer refused the attempt")
	}
	if g.m.State != Submit || g.m.Current != first || !g.m.Sent {
		t.Fatalf("after a timeout: %+v, want Submit on the same bytes, marked sent", g.m)
	}
	if len(g.sends) != 2 || g.sends[1] != first {
		t.Fatalf("sends %v, want the same bytes twice", g.sends)
	}
	// A later "inputs spent" is not proof either.
	acts = g.step(Answered{Out: spentOut})
	if count[Refused](acts) != 0 {
		t.Fatal("inputs spent after an unanswered send refused the attempt")
	}
}

func TestGiveUpAtPatience(t *testing.T) {
	t.Parallel()
	g := newRig(t)
	g.start()
	var last []Action
	for range testPatience {
		last = g.step(Answered{Out: lostOut})
	}
	if g.m.State != GaveUp || g.m.Attempts != testPatience {
		t.Fatalf("state %v attempts %d", g.m.State, g.m.Attempts)
	}
	if len(g.sends) != testPatience {
		t.Fatalf("%d sends, want exactly Patience = %d", len(g.sends), testPatience)
	}
	if count[GiveUp](last) != 1 || count[Frustrate](last) != 1 || count[Refused](last) != 0 {
		t.Fatalf("give-up actions %v, want GiveUp and Frustrate, no release", last)
	}
}

func TestFixesAccumulateInMachine(t *testing.T) {
	t.Parallel()
	g := newRig(t)
	g.start()
	g.step(Answered{Out: feeOut})
	g.step(Answered{Out: Outcome{Verdict: Rejected, Failures: []Failure{{Class: MissingVKeyWitnesses}}}})
	if !g.m.Fixes(FeeTooSmall) || !g.m.Fixes(MissingVKeyWitnesses) || len(g.m.Fixed) != 2 {
		t.Fatalf("Fixed %v, want both classes", g.m.Fixed)
	}
	// A fix named twice stays once.
	m := Machine{State: Diagnosing, Fixed: []Class{FeeTooSmall}}
	m, _, err := Step(DefaultPolicy, m, Diagnosed{Class: FeeTooSmall}, testActor(testPatience), g.r)
	if err != nil || len(m.Fixed) != 1 {
		t.Fatalf("Fixed %v (%v), want one entry", m.Fixed, err)
	}
	// A wrong diagnosis adds only its own class and undoes nothing.
	m.State = Diagnosing
	m, _, _ = Step(DefaultPolicy, m, Diagnosed{Class: Unknown}, testActor(testPatience), g.r)
	if !m.Fixes(FeeTooSmall) || !m.Fixes(Unknown) {
		t.Fatalf("Fixed %v lost an earlier fix", m.Fixed)
	}
}

func TestDoneLearnsOnlyAfterAFix(t *testing.T) {
	t.Parallel()
	g := newRig(t)
	g.start()
	if acts := g.step(Settled{Tx: g.m.Current}); count[Learn](acts) != 0 || g.m.State != Done {
		t.Fatalf("Done with no fix: %v %v", g.m.State, acts)
	}
	h := newRig(t)
	h.start()
	h.step(Answered{Out: feeOut})
	if acts := h.step(Settled{Tx: h.m.Current}); count[Learn](acts) != 1 {
		t.Fatalf("Done after a fix: %v", acts)
	}
}

func TestBackoffGrows(t *testing.T) {
	t.Parallel()
	p := Policy{Base: time.Second, Cap: 10 * time.Second, Jitter: 0}
	r := rand.New(rand.NewPCG(1, 2))
	var prev time.Duration
	for n := 1; n <= 8; n++ {
		d := p.Backoff(n, r)
		switch {
		case n <= 4 && d != time.Second<<(n-1):
			t.Fatalf("backoff(%d) = %v, want %v", n, d, time.Second<<(n-1))
		case n > 4 && d != 10*time.Second:
			t.Fatalf("backoff(%d) = %v, want the cap", n, d)
		case d < prev:
			t.Fatalf("backoff shrank: %v after %v", d, prev)
		}
		prev = d
	}
	if d := p.Backoff(200, r); d != p.Cap {
		t.Fatalf("backoff(200) = %v, overflowed past the cap", d)
	}
	j := Policy{Base: time.Second, Cap: time.Minute, Jitter: 0.25}
	seen := map[time.Duration]bool{}
	for range 20 {
		d := j.Backoff(3, r)
		if d < 3*time.Second || d > 5*time.Second {
			t.Fatalf("jittered backoff %v outside 4s +-25%%", d)
		}
		seen[d] = true
	}
	if len(seen) < 2 {
		t.Fatal("jitter produced one value")
	}
	if d := j.Backoff(20, r); d > j.Cap {
		t.Fatalf("jitter exceeded the cap: %v", d)
	}
}

func TestBackoffEmittedBeforeResend(t *testing.T) {
	t.Parallel()
	g := newRig(t)
	g.start()
	first := g.step(Answered{Out: lostOut})
	if count[Backoff](first) != 1 {
		t.Fatalf("resend actions %v, want one Backoff", first)
	}
	if _, ok := first[0].(Backoff); !ok {
		t.Fatalf("Backoff must precede the SendTx: %v", first)
	}
}

func TestStepTable(t *testing.T) {
	t.Parallel()
	tx := tid(1)
	events := map[string]Event{
		"Start": Start{}, "Built": Built{Tx: tx}, "BuildFailed": BuildFailed{},
		"Accepted": Answered{Out: okOut}, "Unreached": Answered{Out: lostOut}, "Rejected": Answered{Out: feeOut},
		"Diagnosed": Diagnosed{Class: FeeTooSmall}, "InBlock": InBlock{Tx: tx}, "LeftView": LeftView{},
		"Dropped": Dropped{}, "Dead": Dead{}, "Settled": Settled{Tx: tx},
	}
	type want struct {
		next State
		ok   bool
	}
	legal := map[State]map[string]want{
		Build: {"Start": {Build, true}, "Built": {Submit, true}, "BuildFailed": {GaveUp, true},
			"Dead": {Build, true}, "Settled": {Done, true}},
		Submit: {"Accepted": {Pending, true}, "Unreached": {Submit, true}, "Rejected": {Diagnosing, true},
			"Dead": {Build, true}, "Settled": {Done, true}},
		Pending:    {"InBlock": {Confirmed, true}, "Dropped": {Build, true}, "Dead": {Build, true}, "Settled": {Done, true}},
		Confirmed:  {"LeftView": {Submit, true}, "Dead": {Build, true}, "Settled": {Done, true}},
		Diagnosing: {"Diagnosed": {Build, true}, "Dead": {Diagnosing, true}, "Settled": {Done, true}},
	}
	final := map[State]State{Done: Done, GaveUp: GaveUp}
	for _, st := range []State{Build, Submit, Pending, Confirmed, Diagnosing, Done, GaveUp} {
		for name, ev := range events {
			m := Machine{State: st, Current: tx, Attempts: 1, Sent: st != Build}
			got, _, err := Step(DefaultPolicy, m, ev, testActor(testPatience), rand.New(rand.NewPCG(1, 2)))
			if f, ok := final[st]; ok {
				if err != nil || got.State != f {
					t.Errorf("%v + %s: %v, %v; final states ignore every event", st, name, got.State, err)
				}
				continue
			}
			w, ok := legal[st][name]
			switch {
			case !ok && !errors.Is(err, ErrIllegal):
				t.Errorf("%v + %s: err %v, want ErrIllegal", st, name, err)
			case ok && err != nil:
				t.Errorf("%v + %s: unexpected %v", st, name, err)
			case ok && got.State != w.next:
				t.Errorf("%v + %s -> %v, want %v", st, name, got.State, w.next)
			}
		}
	}
}

func TestStepRejectsImpossiblePatience(t *testing.T) {
	t.Parallel()
	// A trait of 0 or less is read as one attempt, never as "no sends".
	if p := Patience(testActor(0)); p != 1 {
		t.Fatalf("Patience(0) = %d, want 1", p)
	}
	if p := Patience(testActor(2.6)); p != 3 {
		t.Fatalf("Patience(2.6) = %d, want 3", p)
	}
}

// PropMachineTerminates: any sequence of node answers, drops, rollbacks and
// planner reports leaves a machine that has not finished within Patience
// sends able to finish, and never lets Attempts pass Patience.
func TestPropMachineTerminates(t *testing.T) {
	t.Parallel()
	for seed := range uint64(300) {
		rr := rand.New(rand.NewPCG(seed, 7))
		g := newRig(t)
		g.r = rand.New(rand.NewPCG(seed, 9))
		g.start()
		for i := 0; i < 200 && g.m.State != Done && g.m.State != GaveUp; i++ {
			g.step(randomEvent(rr, g))
			if g.m.Attempts > testPatience {
				t.Fatalf("seed %d: Attempts %d past Patience %d", seed, g.m.Attempts, testPatience)
			}
		}
		if g.m.State != Done && g.m.State != GaveUp {
			t.Fatalf("seed %d: still %v after 200 events, %d attempts", seed, g.m.State, g.m.Attempts)
		}
	}
}

// randomEvent picks an event legal in the machine's state.
func randomEvent(r *rand.Rand, g *rig) Event {
	outs := []Outcome{okOut, lostOut, feeOut, spentOut, mempOut}
	switch g.m.State {
	case Submit:
		switch r.IntN(8) {
		case 0:
			return Settled{Tx: g.m.Current}
		case 1:
			return Dead{}
		}
		return Answered{Out: outs[r.IntN(len(outs))]}
	case Pending:
		return []Event{InBlock{Tx: g.m.Current}, Dropped{}, Dead{}, Settled{Tx: g.m.Current}}[r.IntN(4)]
	case Confirmed:
		return []Event{LeftView{}, LeftView{}, Dead{}, Settled{Tx: g.m.Current}}[r.IntN(4)]
	default:
		return Start{}
	}
}
