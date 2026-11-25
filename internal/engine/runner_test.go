package engine

import (
	"bytes"
	"context"
	"errors"
	"math/rand/v2"
	"testing"

	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/fakechain"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
	"github.com/chrisguiney/cardano-continuum/internal/store"
)

func cleanRun(t *testing.T, w *world, until sched.Time) *proc {
	t.Helper()
	p, err := w.start(nil, 50)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.r.Run(context.Background(), until); err != nil {
		t.Fatal(err)
	}
	return p
}

func sameBalances(a, b map[uint64]uint64) bool {
	for k, v := range a {
		if v != 0 && b[k] != v {
			return false
		}
	}
	for k, v := range b {
		if v != 0 && a[k] != v {
			return false
		}
	}
	return true
}

// TestRestartAfterOfflineRollback: the chain rolls back past the stored
// cursor while the engine is down. After restart the engine's cursor equals
// the chain's tip and its actors equal Derive of the new chain.
func TestRestartAfterOfflineRollback(t *testing.T) {
	w := newWorld(t, testConfig(t, 5, 6))
	p := cleanRun(t, w, 120_000)
	oldTip := p.r.Engine().Tip()
	p.stop(t)

	if err := w.chain.Inject(fakechain.Rollback{Depth: 2}); err != nil {
		t.Fatal(err)
	}
	// The chain keeps forging while the engine is down.
	down := w.cfg.Slots.SlotStart(w.chain.Slot() + 3*w.cfg.BlockEvery)
	if err := w.clock.Wait(context.Background(), down); err != nil {
		t.Fatal(err)
	}
	if err := w.chain.AdvanceToTime(down); err != nil {
		t.Fatal(err)
	}

	p = cleanRun(t, w, 200_000)
	defer p.stop(t)
	e := p.r.Engine()
	tip := w.chain.Tip()
	if e.Tip().Slot != tip.Slot || !bytes.Equal(e.Tip().Hash, tip.Hash) {
		t.Fatalf("engine tip %d, chain tip %d", e.Tip().Slot, tip.Slot)
	}
	if samePoint(e.Tip(), oldTip) {
		t.Fatal("the chain did not move past the old tip")
	}
	if want, got := derive(t, w.cfg, w.chain), e.Balances(); !sameBalances(want, got) {
		t.Fatalf("actors differ from Derive(chain):\nengine %v\nchain  %v", got, want)
	}
	var rolled bool
	for _, r := range logRecords(t, p.log) {
		rolled = rolled || r.Kind == eventlog.KindRolledBack
	}
	if !rolled {
		t.Fatal("no RolledBack input was logged")
	}
}

// TestIntersectionHandledAsRollback: when FindIntersect answers below the
// tip, the engine logs the rollback and unwinds to it before any new block.
func TestIntersectionHandledAsRollback(t *testing.T) {
	w := newWorld(t, testConfig(t, 6, 4))
	p := cleanRun(t, w, 60_000)
	p.stop(t)
	if err := w.chain.Inject(fakechain.Rollback{Depth: 2}); err != nil {
		t.Fatal(err)
	}
	want := w.chain.Tip()

	p, err := w.start(nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer p.stop(t)
	recs := logRecords(t, p.log)
	last := recs[len(recs)-1]
	if last.Kind != eventlog.KindRolledBack || last.Class != eventlog.Input {
		t.Fatalf("last record after restart is %s %d, want the RolledBack input", last.Kind, last.Class)
	}
	var rb eventlog.RolledBack
	if err := eventlog.Decode(last.Body, &rb); err != nil {
		t.Fatal(err)
	}
	if rb.To.Slot != want.Slot || !bytes.Equal(rb.To.Hash, want.Hash) {
		t.Fatalf("RolledBack to slot %d, want the intersection %d", rb.To.Slot, want.Slot)
	}
	if tip := p.r.Engine().Tip(); tip.Slot != want.Slot {
		t.Fatalf("engine tip %d before the next block, want %d", tip.Slot, want.Slot)
	}
}

// checkingStore asserts, at every checkpoint, that the checkpoint's seq is
// durable in the log and that the snapshot is what a replay of the log up to
// that seq produces.
type checkingStore struct {
	t     *testing.T
	cfg   Config
	log   *eventlog.MemLog
	saves int
}

func (c *checkingStore) SaveCheckpoint(ctx context.Context, cp store.Checkpoint) error {
	c.saves++
	if cp.Seq > c.log.DurableSeq() {
		c.t.Fatalf("checkpoint seq %d above durable seq %d", cp.Seq, c.log.DurableSeq())
	}
	res, err := (&eventlog.Replayer{New: c.cfg.Factory(), Log: upTo{c.log, cp.Seq}}).Replay(ctx, nil, 0)
	if err != nil {
		c.t.Fatal(err)
	}
	if !bytes.Equal(res.State, cp.Actors) {
		c.t.Fatalf("checkpoint at seq %d is not the replay of the log up to it", cp.Seq)
	}
	return nil
}

func (c *checkingStore) Recover(context.Context) (store.Recovery, error) {
	return store.Recovery{}, nil
}

type upTo struct {
	r   eventlog.Reader
	max uint64
}

func (u upTo) Scan(ctx context.Context, from uint64, fn func(eventlog.Record) error) error {
	return u.r.Scan(ctx, from, func(rec eventlog.Record) error {
		if rec.Seq > u.max {
			return eventlog.ErrStop
		}
		return fn(rec)
	})
}

func TestCheckpointWaitsForLogFlush(t *testing.T) {
	cfg := testConfig(t, 7, 4)
	w := newWorld(t, cfg)
	l := &eventlog.MemLog{}
	cs := &checkingStore{t: t, cfg: cfg, log: l}
	r, err := Start(context.Background(), cfg, Deps{Clock: w.clock, Log: l, Store: cs, Node: w.node, Pacer: w.node, CheckpointEvery: 40})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Run(context.Background(), 90_000); err != nil {
		t.Fatal(err)
	}
	if cs.saves < 3 {
		t.Fatalf("only %d checkpoints", cs.saves)
	}
}

// TestCrashInjectionConverges: with kills after random store and log writes
// and a bounded number of crashes, the engine ends at the chain's tip,
// flushed and checkpointed.
func TestCrashInjectionConverges(t *testing.T) {
	for seed := uint64(1); seed <= 4; seed++ {
		w := newWorld(t, testConfig(t, seed, 5))
		r := rand.New(rand.NewPCG(seed, 99))
		left := 6
		crashes := w.run(150_000, 7, func(int) bool {
			if left > 0 && r.IntN(40) == 0 {
				left--
				return true
			}
			return false
		})
		if crashes == 0 {
			t.Fatalf("seed %d: no crash was injected", seed)
		}
		p, err := w.start(nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		tip := w.chain.Tip()
		if e := p.r.Engine().Tip(); e.Slot != tip.Slot || !bytes.Equal(e.Hash, tip.Hash) {
			t.Fatalf("seed %d: engine tip %d, chain tip %d", seed, e.Slot, tip.Slot)
		}
		cp, err := p.db.LoadCheckpoint(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if cp.Seq != p.log.LastSeq() {
			t.Fatalf("seed %d: checkpoint covers %d of %d", seed, cp.Seq, p.log.LastSeq())
		}
		p.stop(t)
	}
}

// TestShutdownOrder: cancelling the loop flushes the log and checkpoints
// everything before Run returns.
func TestShutdownOrder(t *testing.T) {
	w := newWorld(t, testConfig(t, 8, 4))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n := 0
	p, err := w.start(func(string) error {
		if n++; n == 60 {
			cancel()
		}
		return nil
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer p.stop(t)
	if err := p.r.Run(ctx, 1_000_000); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v, want context.Canceled", err)
	}
	if p.log.DurableSeq() != p.log.LastSeq() {
		t.Fatalf("log not flushed: %d of %d", p.log.DurableSeq(), p.log.LastSeq())
	}
	cp, err := p.db.LoadCheckpoint(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cp.Seq != p.log.LastSeq() {
		t.Fatalf("checkpoint covers %d of %d", cp.Seq, p.log.LastSeq())
	}
}

func TestHeaderMismatchRefused(t *testing.T) {
	w := newWorld(t, testConfig(t, 9, 3))
	cleanRun(t, w, 10_000).stop(t)
	w.cfg.Seed = SeedFromInt(10)
	if _, err := w.start(nil, 0); !errors.Is(err, eventlog.ErrHeaderMismatch) {
		t.Fatalf("restart under another seed: %v", err)
	}
}
