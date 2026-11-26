package engine

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"testing"
	"time"

	"github.com/blinklabs-io/gouroboros/ledger/conway"

	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/fakechain"
	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
	"github.com/chrisguiney/cardano-continuum/internal/submit"
)

// The tests in this file pin when a payment's reservation is released, the
// rules of models/specs/planner-reservations and submit-retry: only a
// rejection the answer proves never arrived (a first send, or a rule that
// reads the transaction alone) or a settled conflict or expiry releases;
// giving up releases nothing (PlannerReleaseOnSubmitErrorBug,
// PlannerExpireAtTipBug, RetryReleaseOnGiveUpBug).

// scriptedNode answers the n-th Submit (counted from 1) with answer, which
// may deliver the transaction to the chain before it returns.
type scriptedNode struct {
	*FakeNode
	n      int
	answer func(ctx context.Context, n int, tx []byte) error
}

func (s *scriptedNode) Submit(ctx context.Context, tx []byte) error {
	s.n++
	return s.answer(ctx, s.n, tx)
}

// payments reads the log's Submitted, Confirmed and Released decisions by
// intent.
type payments struct {
	submitted []Submitted
	confirmed map[uint64]int
	released  map[uint64][]string
	refused   map[TxID]int
	gaveUp    map[uint64]int
	actor     map[uint64]uint64 // intent to actor
}

func readPayments(t *testing.T, l eventlog.Reader) payments {
	t.Helper()
	p := payments{confirmed: map[uint64]int{}, released: map[uint64][]string{}, refused: map[TxID]int{}, gaveUp: map[uint64]int{}, actor: map[uint64]uint64{}}
	for _, rec := range logRecords(t, l) {
		switch rec.Kind {
		case KindSubmitted:
			var s Submitted
			if err := eventlog.Decode(rec.Body, &s); err != nil {
				t.Fatal(err)
			}
			p.submitted = append(p.submitted, s)
			p.actor[s.Intent] = uint64(rec.Actor)
		case KindConfirmed:
			var c Confirmed
			if err := eventlog.Decode(rec.Body, &c); err != nil {
				t.Fatal(err)
			}
			p.confirmed[c.Intent]++
		case KindRefused:
			var r Refused
			if err := eventlog.Decode(rec.Body, &r); err != nil {
				t.Fatal(err)
			}
			p.refused[r.ID]++
		case KindGaveUp:
			var g GaveUp
			if err := eventlog.Decode(rec.Body, &g); err != nil {
				t.Fatal(err)
			}
			p.gaveUp[g.Intent]++
		case KindReleased:
			var r Released
			if err := eventlog.Decode(rec.Body, &r); err != nil {
				t.Fatal(err)
			}
			p.released[r.Intent] = append(p.released[r.Intent], r.Why)
		}
	}
	if len(p.submitted) == 0 {
		t.Fatal("no payment was submitted")
	}
	return p
}

// paidOnce checks the intent kept its reservation and confirmed exactly
// once, and that its first attempt, which may have reached a node, was never
// refused.
func (p payments) paidOnce(t *testing.T, intent uint64) {
	t.Helper()
	if why := p.released[intent]; len(why) > 0 {
		t.Errorf("intent %d released (%v) though its transaction may have reached a node", intent, why)
	}
	if n := p.confirmed[intent]; n != 1 {
		t.Errorf("intent %d confirmed %d times, want 1", intent, n)
	}
	if n := p.refused[p.submitted[0].ID]; n > 0 {
		t.Errorf("intent %d: the first attempt, which may have reached a node, was refused", intent)
	}
}

// TestTransportErrorKeepsReservation: the first submission reaches the
// chain but the connection breaks before the answer. That is Unreached, not
// a refusal: the reservation stays and the payment confirms once.
func TestTransportErrorKeepsReservation(t *testing.T) {
	w := newWorld(t, testConfig(t, 5, 6))
	sn := &scriptedNode{FakeNode: w.node}
	sn.answer = func(ctx context.Context, n int, tx []byte) error {
		err := w.node.Submit(ctx, tx)
		if n == 1 && err == nil {
			return errors.New("write tcp 127.0.0.1:3001: connection reset by peer")
		}
		return err
	}
	l := &eventlog.MemLog{}
	r, err := Start(context.Background(), w.cfg, Deps{Clock: w.clock, Log: l, Node: sn, Pacer: w.node})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Run(context.Background(), 120_000); err != nil {
		t.Fatal(err)
	}
	p := readPayments(t, l)
	p.paidOnce(t, p.submitted[0].Intent)
	if e := r.Engine(); e.Supply() != genesisTotal(t, w.cfg) {
		t.Fatalf("supply %d, genesis %d", e.Supply(), genesisTotal(t, w.cfg))
	}
}

// TestResendOfIncludedTxKeepsReservation: the engine dies after its first
// submission reached the node; the transaction lands while it is down. The
// resubmission after the restart is rejected because the transaction itself
// spent the inputs, which proves nothing: the reservation stays and the
// payment confirms once.
func TestResendOfIncludedTxKeepsReservation(t *testing.T) {
	w := newWorld(t, testConfig(t, 5, 6))
	w.mem = &memStore{log: w.log}
	crashed := false
	p, err := w.start(func(stage string) error {
		if stage == StageSubmitted && !crashed {
			crashed = true
			return errCrash
		}
		return nil
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.r.Run(context.Background(), 120_000); !errors.Is(err, errCrash) {
		t.Fatalf("Run = %v, want the injected crash", err)
	}
	p.kill(t)
	if len(w.chain.Mempool()) == 0 {
		t.Fatal("the first submission is not in the mempool")
	}
	// The node forges the transaction while the engine is down.
	for len(w.chain.Mempool()) > 0 {
		next := w.node.NextChange(w.clock.Now())
		if err := w.clock.Wait(context.Background(), next); err != nil {
			t.Fatal(err)
		}
		if err := w.chain.AdvanceToTime(next); err != nil {
			t.Fatal(err)
		}
	}
	w.run(200_000, 0, nil)
	pays := readPayments(t, w.log)
	pays.paidOnce(t, pays.submitted[0].Intent)
}

// TestFirstSendRejectionRefuses: a node refusing the first submission is
// proof the attempt never reached a mempool, so the ledger writes it off and
// the machine builds the next attempt, which confirms.
func TestFirstSendRejectionRefuses(t *testing.T) {
	w := newWorld(t, testConfig(t, 5, 6))
	sn := &scriptedNode{FakeNode: w.node}
	sn.answer = func(ctx context.Context, n int, tx []byte) error {
		if n == 1 {
			return &submit.RejectedError{Failures: []submit.Failure{{Class: submit.FeeTooSmall}}}
		}
		return w.node.Submit(ctx, tx)
	}
	l := &eventlog.MemLog{}
	r, err := Start(context.Background(), w.cfg, Deps{Clock: w.clock, Log: l, Node: sn, Pacer: w.node})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Run(context.Background(), 240_000); err != nil {
		t.Fatal(err)
	}
	p := readPayments(t, l)
	first := p.submitted[0]
	if p.refused[first.ID] != 1 {
		t.Fatalf("first attempt refused %d times, want once", p.refused[first.ID])
	}
	if why := p.released[first.Intent]; len(why) != 0 {
		t.Fatalf("intent %d released %v: a refusal builds the next attempt, it does not end the payment", first.Intent, why)
	}
	if p.confirmed[first.Intent] != 1 {
		t.Fatalf("intent %d confirmed %d times, want the rebuilt attempt once", first.Intent, p.confirmed[first.Intent])
	}
	if e := r.Engine(); e.Supply() != genesisTotal(t, w.cfg) {
		t.Fatalf("supply %d, genesis %d", e.Supply(), genesisTotal(t, w.cfg))
	}
}

// TestIntrinsicRejectionAfterUnreachedRefuses: a resend rejected with a rule
// that reads the transaction alone proves the bytes never landed, even after
// an unanswered send.
func TestIntrinsicRejectionAfterUnreachedRefuses(t *testing.T) {
	w := newWorld(t, testConfig(t, 5, 6))
	sn := &scriptedNode{FakeNode: w.node}
	sn.answer = func(ctx context.Context, n int, tx []byte) error {
		switch n {
		case 1:
			return errors.New("connection reset by peer")
		case 2:
			return &submit.RejectedError{Failures: []submit.Failure{{Class: submit.FeeTooSmall}}}
		}
		return w.node.Submit(ctx, tx)
	}
	l := &eventlog.MemLog{}
	r, err := Start(context.Background(), w.cfg, Deps{Clock: w.clock, Log: l, Node: sn, Pacer: w.node})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Run(context.Background(), 240_000); err != nil {
		t.Fatal(err)
	}
	p := readPayments(t, l)
	if first := p.submitted[0]; p.refused[first.ID] != 1 {
		t.Fatalf("first attempt refused %d times, want once", p.refused[first.ID])
	}
}

// TestGiveUpReleasesNothing: a node that never answers exhausts the actor's
// patience. The engine stops sending, records GaveUp once, and the
// reservation stays until the ledger writes the attempt off at settled
// expiry (RetryReleaseOnGiveUpBug).
func TestGiveUpReleasesNothing(t *testing.T) {
	cfg := testConfig(t, 5, 6)
	// Ten sends fit well inside the 30-slot TTL.
	cfg.Retry = submit.Policy{Base: 100 * time.Millisecond, Cap: 100 * time.Millisecond}
	w := newWorld(t, cfg)
	sn := &scriptedNode{FakeNode: w.node}
	var sent [][]byte
	sn.answer = func(_ context.Context, _ int, tx []byte) error {
		sent = append(sent, tx)
		return errors.New("connection reset by peer")
	}
	l := &eventlog.MemLog{}
	r, err := Start(context.Background(), w.cfg, Deps{Clock: w.clock, Log: l, Node: sn, Pacer: w.node})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Run(context.Background(), 900_000); err != nil {
		t.Fatal(err)
	}
	p := readPayments(t, l)
	id := p.submitted[0].Intent
	if p.gaveUp[id] != 1 {
		t.Fatalf("intent %d gave up %d times, want once", id, p.gaveUp[id])
	}
	if why := p.released[id]; len(why) != 1 || why[0] != "expired" {
		t.Fatalf("intent %d released %v, want one settled expiry and nothing at give-up", id, why)
	}
	n := 0
	for _, tx := range sent {
		if bytes.Equal(tx, p.submitted[0].Tx) {
			n++
		}
	}
	if n != 10 {
		t.Fatalf("the first attempt was sent %d times, want exactly the actor's patience (10)", n)
	}
}

// slotPoint is a block point at slot with a hash naming n.
func slotPoint(slot, n uint64) eventlog.ChainPoint {
	return eventlog.ChainPoint{Slot: slot, Hash: binary.BigEndian.AppendUint64(make([]byte, 24), n)}
}

func blockAt(t *testing.T, p eventlog.ChainPoint) eventlog.Record {
	t.Helper()
	content, err := eventlog.Encode(BlockContent{})
	if err != nil {
		t.Fatal(err)
	}
	body, err := eventlog.Encode(eventlog.BlockApplied{Point: p, Content: content})
	if err != nil {
		t.Fatal(err)
	}
	return eventlog.Record{Class: eventlog.Input, Kind: eventlog.KindBlockApplied, Body: body}
}

func released(t *testing.T, out []eventlog.Record, intent uint64) []string {
	t.Helper()
	var why []string
	for _, rec := range out {
		if rec.Kind != KindReleased {
			continue
		}
		var r Released
		if err := eventlog.Decode(rec.Body, &r); err != nil {
			t.Fatal(err)
		}
		if r.Intent == intent {
			why = append(why, r.Why)
		}
	}
	return why
}

// TestExpiryAtSettledTTL: a payment valid only below slot TTL is dead once
// the settled block's slot reaches TTL, not one block later. Dead, its
// machine builds the next attempt (submit-retry: Submit, dead -> Build).
func TestExpiryAtSettledTTL(t *testing.T) {
	cfg := testConfig(t, 3, 8)
	cfg.K = 2
	e, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var sub *Submitted
	for at := sched.Time(1000); sub == nil && at < 1_000_000; at += 1000 {
		for _, rec := range mustHandle(t, e, tickInput(at)) {
			if rec.Kind == KindSubmitted && sub == nil {
				sub = &Submitted{}
				if err := eventlog.Decode(rec.Body, sub); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	if sub == nil {
		t.Fatal("no payment was submitted")
	}
	// Blocks at TTL-1, TTL, TTL+1, TTL+2: with K = 2 the third settles the
	// block at TTL-1 and the fourth the block at TTL.
	for i, slot := range []uint64{sub.TTL - 1, sub.TTL, sub.TTL + 1, sub.TTL + 2} {
		out := mustHandle(t, e, blockAt(t, slotPoint(slot, uint64(i+1))))
		dead := len(released(t, out, sub.Intent)) > 0 || rebuilt(t, out, *sub)
		switch {
		case i < 3 && dead:
			t.Fatalf("written off at block %d (slot %d), before the settled slot reached TTL %d", i+1, slot, sub.TTL)
		case i == 3 && !dead:
			t.Fatalf("not written off when the block at TTL %d settled", sub.TTL)
		}
	}
}

// TestRefusedGiveUpReleasesOnce: every node refuses every attempt, so the
// actor rebuilds until its patience runs out. The give-up happens inside
// the diagnosis of the last refusal, and the payment is released once.
func TestRefusedGiveUpReleasesOnce(t *testing.T) {
	cfg := testConfig(t, 5, 6)
	cfg.Retry = submit.Policy{Base: 100 * time.Millisecond, Cap: 100 * time.Millisecond}
	w := newWorld(t, cfg)
	sn := &scriptedNode{FakeNode: w.node}
	sn.answer = func(context.Context, int, []byte) error {
		return &submit.RejectedError{Failures: []submit.Failure{{Class: submit.FeeTooSmall}}}
	}
	l := &eventlog.MemLog{}
	r, err := Start(context.Background(), w.cfg, Deps{Clock: w.clock, Log: l, Node: sn, Pacer: w.node})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Run(context.Background(), 300_000); err != nil {
		t.Fatal(err)
	}
	p := readPayments(t, l)
	gave := 0
	for id, n := range p.gaveUp {
		gave += n
		if why := p.released[id]; len(why) != 1 || why[0] != "gave up" {
			t.Errorf("intent %d released %v, want once on give-up", id, why)
		}
	}
	if gave == 0 {
		t.Fatal("no intent gave up")
	}
	// Giving up frustrates the actor (submit.Frustrate).
	a, _ := r.Engine().census.Get(population.ActorID(p.actor[p.submitted[0].Intent]))
	if a.Frustration <= 0 {
		t.Fatalf("actor %d gave up with frustration %v", a.ID, a.Frustration)
	}
}

// TestFixThatWorksTeaches: the first attempt is refused for its fee; the
// actor fixes it, the next attempt settles, and the actor's competence
// rises (submit.Learn).
func TestFixThatWorksTeaches(t *testing.T) {
	cfg := testConfig(t, 9, 4)
	w := newWorld(t, cfg)
	sn := &scriptedNode{FakeNode: w.node}
	sn.answer = func(ctx context.Context, n int, tx []byte) error {
		if n == 1 {
			return &submit.RejectedError{Failures: []submit.Failure{{Class: submit.FeeTooSmall}}}
		}
		return w.node.Submit(ctx, tx)
	}
	l := &eventlog.MemLog{}
	r, err := Start(context.Background(), w.cfg, Deps{Clock: w.clock, Log: l, Node: sn, Pacer: w.node})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Run(context.Background(), 150_000); err != nil {
		t.Fatal(err)
	}
	p := readPayments(t, l)
	s := p.submitted[0]
	if p.confirmed[s.Intent] != 1 || p.refused[s.ID] != 1 {
		t.Fatalf("intent %d: confirmed %d, first attempt refused %d", s.Intent, p.confirmed[s.Intent], p.refused[s.ID])
	}
	a, _ := r.Engine().census.Get(population.ActorID(p.actor[s.Intent]))
	if a.Competence <= a.Traits.Competence {
		t.Fatalf("competence %v, drawn %v: the fix taught nothing", a.Competence, a.Traits.Competence)
	}
}

// rebuilt reports a new attempt of sub's intent among out.
func rebuilt(t *testing.T, out []eventlog.Record, sub Submitted) bool {
	t.Helper()
	for _, rec := range out {
		if rec.Kind != KindSubmitted {
			continue
		}
		var s Submitted
		if err := eventlog.Decode(rec.Body, &s); err != nil {
			t.Fatal(err)
		}
		if s.Intent == sub.Intent && s.ID != sub.ID {
			return true
		}
	}
	return false
}

// TestDropRebuilds: the first payment leaves the mempool without a block
// after the node accepted it. The drop notice moves its machine to build
// the next attempt through Reserve, which lands, and the payment confirms
// once.
func TestDropRebuilds(t *testing.T) {
	cfg := testConfig(t, 7, 4)
	w := newWorld(t, cfg)
	sn := &scriptedNode{FakeNode: w.node}
	var dropped TxID
	sn.answer = func(ctx context.Context, n int, tx []byte) error {
		err := w.node.Submit(ctx, tx)
		if n == 1 && err == nil {
			ct, derr := conway.NewConwayTransactionFromCbor(tx)
			if derr != nil {
				t.Fatal(derr)
			}
			dropped = TxID(ct.Hash())
			if ierr := w.chain.Inject(fakechain.Drop{Tx: ct.Hash()}); ierr != nil {
				t.Fatal(ierr)
			}
		}
		return err
	}
	l := &eventlog.MemLog{}
	r, err := Start(context.Background(), w.cfg, Deps{Clock: w.clock, Log: l, Node: sn, Pacer: w.node})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Run(context.Background(), 150_000); err != nil {
		t.Fatal(err)
	}
	p := readPayments(t, l)
	if p.submitted[0].ID != dropped {
		t.Fatal("the dropped transaction is not the first attempt")
	}
	id := p.submitted[0].Intent
	attempts := 0
	for _, s := range p.submitted {
		if s.Intent == id {
			attempts++
		}
	}
	if attempts < 2 {
		t.Fatalf("intent %d: %d attempts, want a rebuild after the drop", id, attempts)
	}
	if p.confirmed[id] != 1 {
		t.Fatalf("intent %d confirmed %d times, want once", id, p.confirmed[id])
	}
}

// firstPayment ticks e until it submits a payment and returns it.
func firstPayment(t *testing.T, e *Engine) Submitted {
	t.Helper()
	for at := sched.Time(1000); at < 1_000_000; at += 1000 {
		for _, rec := range mustHandle(t, e, tickInput(at)) {
			if rec.Kind == KindSubmitted {
				var s Submitted
				if err := eventlog.Decode(rec.Body, &s); err != nil {
					t.Fatal(err)
				}
				return s
			}
		}
	}
	t.Fatal("no payment was submitted")
	return Submitted{}
}

func answer(t *testing.T, r SubmitResult) eventlog.Record {
	t.Helper()
	body, err := eventlog.Encode(r)
	if err != nil {
		t.Fatal(err)
	}
	return eventlog.Record{Class: eventlog.Input, Kind: eventlog.KindSubmitResult, Body: body}
}

// TestRolledBackAttemptIsSentAgain: an accepted payment in a block that a
// rollback removes goes out again as the same bytes, with nothing released
// and no new attempt built (submit-retry: Confirmed, left the view ->
// Submit).
func TestRolledBackAttemptIsSentAgain(t *testing.T) {
	cfg := testConfig(t, 3, 8)
	e, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	sub := firstPayment(t, e)
	mustHandle(t, e, answer(t, SubmitResult{Intent: sub.Intent, Verdict: VerdictAccepted}))
	if len(e.InFlight()) != 0 {
		t.Fatal("an accepted payment is still in flight")
	}
	before := e.Tip()
	content, err := eventlog.Encode(BlockContent{Txs: []TxDigest{{ID: sub.ID, Inputs: sub.Inputs}}})
	if err != nil {
		t.Fatal(err)
	}
	body, err := eventlog.Encode(eventlog.BlockApplied{Point: slotPoint(sub.TTL-5, 1), Content: content})
	if err != nil {
		t.Fatal(err)
	}
	mustHandle(t, e, eventlog.Record{Class: eventlog.Input, Kind: eventlog.KindBlockApplied, Body: body})
	if got := e.pending[sub.Intent].M.State; got != submit.Confirmed {
		t.Fatalf("machine %v after the block, want Confirmed", got)
	}
	out := mustHandle(t, e, rollbackInput(t, before))
	if len(released(t, out, sub.Intent)) > 0 || rebuilt(t, out, sub) {
		t.Fatal("the rollback released or rebuilt the payment")
	}
	// The resend waits out its backoff first.
	mustHandle(t, e, tickInput(max(e.pending[sub.Intent].NotBefore, e.Now())))
	inf := e.InFlight()
	if len(inf) != 1 || inf[0].ID != sub.ID || !bytes.Equal(inf[0].Tx, sub.Tx) {
		t.Fatalf("in flight %+v, want the same bytes again", inf)
	}
}
