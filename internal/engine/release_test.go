package engine

import (
	"context"
	"encoding/binary"
	"errors"
	"strings"
	"testing"

	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
	"github.com/chrisguiney/cardano-continuum/internal/submit"
)

// The tests in this file pin when a payment's reservation is released, the
// rules of models/specs/planner-reservations: only a first-send rejection
// or a settled conflict or expiry releases (PlannerReleaseOnSubmitErrorBug,
// PlannerExpireAtTipBug).

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
}

func readPayments(t *testing.T, l eventlog.Reader) payments {
	t.Helper()
	p := payments{confirmed: map[uint64]int{}, released: map[uint64][]string{}}
	for _, rec := range logRecords(t, l) {
		switch rec.Kind {
		case KindSubmitted:
			var s Submitted
			if err := eventlog.Decode(rec.Body, &s); err != nil {
				t.Fatal(err)
			}
			p.submitted = append(p.submitted, s)
		case KindConfirmed:
			var c Confirmed
			if err := eventlog.Decode(rec.Body, &c); err != nil {
				t.Fatal(err)
			}
			p.confirmed[c.Intent]++
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
// once, and that no payment was refused for spending a released input.
func (p payments) paidOnce(t *testing.T, intent uint64) {
	t.Helper()
	if why := p.released[intent]; len(why) > 0 {
		t.Errorf("intent %d released (%v) though its transaction may have reached a node", intent, why)
	}
	if n := p.confirmed[intent]; n != 1 {
		t.Errorf("intent %d confirmed %d times, want 1", intent, n)
	}
	for id, why := range p.released {
		for _, w := range why {
			if strings.HasPrefix(w, "rejected") {
				t.Errorf("intent %d refused (%s): an input was offered while still reserved", id, w)
			}
		}
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

// TestFirstSendRejectionReleases: a node refusing the first submission is
// proof the transaction never reached a mempool, so the reservation goes.
func TestFirstSendRejectionReleases(t *testing.T) {
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
	if err := r.Run(context.Background(), 120_000); err != nil {
		t.Fatal(err)
	}
	p := readPayments(t, l)
	id := p.submitted[0].Intent
	if why := p.released[id]; len(why) != 1 || !strings.HasPrefix(why[0], "rejected") {
		t.Fatalf("intent %d released %v, want one rejection", id, why)
	}
	if p.confirmed[id] != 0 {
		t.Fatalf("a refused intent confirmed")
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
// the settled block's slot reaches TTL, not one block later.
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
		why := released(t, mustHandle(t, e, blockAt(t, slotPoint(slot, uint64(i+1)))), sub.Intent)
		switch {
		case i < 3 && len(why) > 0:
			t.Fatalf("released at block %d (slot %d), before the settled slot reached TTL %d", i+1, slot, sub.TTL)
		case i == 3 && len(why) != 1:
			t.Fatalf("not released when the block at TTL %d settled: %v", sub.TTL, why)
		}
	}
}
