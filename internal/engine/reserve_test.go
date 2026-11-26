package engine

import (
	"context"
	"errors"
	"testing"

	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/plan"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
	"github.com/chrisguiney/cardano-continuum/internal/store"
)

// refusing is a ledger Committer whose every commit fails, so every
// Reserve fails and leaves the ledger as it was.
type refusing struct{}

func (refusing) Commit(context.Context, store.Blob) error { return errors.New("commit refused") }

// refuseReserves swaps e's ledger for one with the same state whose Reserve
// always fails (models/specs/planner-reservations: a refused Reserve means
// the transaction must not be sent).
func refuseReserves(t *testing.T, e *Engine) {
	t.Helper()
	blob, err := e.led.Blob()
	if err != nil {
		t.Fatal(err)
	}
	cfg := e.ledgerConfig()
	cfg.Store = refusing{}
	if e.led, err = plan.Open(cfg, blob); err != nil {
		t.Fatal(err)
	}
}

func count(out []eventlog.Record, kind string) int {
	n := 0
	for _, rec := range out {
		if rec.Kind == kind {
			n++
		}
	}
	return n
}

// TestFirstBuildNeedsReserve: when Reserve refuses, the first attempt of an
// intent is not submitted and the intent does not exist.
func TestFirstBuildNeedsReserve(t *testing.T) {
	t.Parallel()
	e, err := New(testConfig(t, 3, 8))
	if err != nil {
		t.Fatal(err)
	}
	refuseReserves(t, e)
	for at := sched.Time(1000); at < 400_000; at += 1000 {
		out := mustHandle(t, e, tickInput(at))
		if n := count(out, KindSubmitted); n > 0 {
			t.Fatalf("at %d: %d payments submitted although Reserve refuses", at, n)
		}
	}
	if len(e.pending) != 0 || len(e.InFlight()) != 0 {
		t.Fatalf("%d pending, %d in flight, want none", len(e.pending), len(e.InFlight()))
	}
}

// TestRebuildNeedsReserve: after a drop, the next attempt goes through
// Reserve; when it refuses, no new attempt is submitted and the intent
// gives up.
func TestRebuildNeedsReserve(t *testing.T) {
	t.Parallel()
	e, err := New(testConfig(t, 3, 8))
	if err != nil {
		t.Fatal(err)
	}
	sub := firstPayment(t, e)
	mustHandle(t, e, answer(t, SubmitResult{Intent: sub.Intent, Verdict: VerdictAccepted}))
	refuseReserves(t, e)
	out := mustHandle(t, e, answer(t, SubmitResult{Intent: sub.Intent, Verdict: VerdictDropped, Tx: &sub.ID}))
	for at := sched.Time(1000); at < 120_000 && count(out, KindGaveUp) == 0; at += 1000 {
		out = append(out, mustHandle(t, e, tickInput(max(at, e.Now())))...)
	}
	if rebuilt(t, out, sub) {
		t.Fatal("a new attempt was submitted although Reserve refuses")
	}
	if count(out, KindGaveUp) == 0 {
		t.Fatal("the intent did not give up after Reserve refused the rebuild")
	}
}
