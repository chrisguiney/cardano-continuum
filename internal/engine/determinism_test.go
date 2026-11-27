package engine

import (
	"bytes"
	"context"
	"fmt"
	"testing"

	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

// The determinism tests share the name prefix TestDeterminism so `make
// determinism` can select them.

func countKinds(t testing.TB, r eventlog.Reader) map[string]int {
	t.Helper()
	n := map[string]int{}
	for _, rec := range logRecords(t, r) {
		n[rec.Kind]++
	}
	return n
}

func TestDeterminismSameSeedSameLog(t *testing.T) {
	t.Parallel()
	var logs [][]byte
	for range 2 {
		w := newWorld(t, testConfig(t, 11, 8))
		w.run(180_000, 25, nil)
		logs = append(logs, w.logBytes())
		if k := countKinds(t, w.log); k[KindSubmitted] == 0 || k[KindConfirmed] == 0 || k[KindSpawned] == 0 {
			t.Fatalf("the run does not exercise the engine: %v", k)
		}
	}
	if !bytes.Equal(logs[0], logs[1]) {
		t.Fatal("two runs with one seed wrote different logs")
	}
}

func TestDeterminismSeedSensitivity(t *testing.T) {
	t.Parallel()
	var decisions [2][]eventlog.Record
	for i, seed := range []uint64{1, 2} {
		w := newWorld(t, testConfig(t, seed, 8))
		w.run(30_000, 0, nil)
		for _, r := range logRecords(t, w.log) {
			if r.Class == eventlog.Decision && len(decisions[i]) < 50 {
				decisions[i] = append(decisions[i], r)
			}
		}
	}
	if len(decisions[0]) < 50 || len(decisions[1]) < 50 {
		t.Fatalf("too few decisions: %d, %d", len(decisions[0]), len(decisions[1]))
	}
	for i := range decisions[0] {
		if !sameRecord(decisions[0][i], decisions[1][i]) {
			return
		}
	}
	t.Fatal("seeds 1 and 2 agree on the first 50 decisions")
}

func TestDeterminismReplayReproduces(t *testing.T) {
	t.Parallel()
	w := newWorld(t, testConfig(t, 12, 10))
	w.run(240_000, 0, nil)
	diff, err := (&eventlog.Replayer{New: w.cfg.Factory(), Log: w.log}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(diff) > 0 {
		t.Fatalf("replay diverged at %v (%d mismatches)", diff[0], len(diff))
	}
	if n := len(logRecords(t, w.log)); n < 500 {
		t.Fatalf("only %d records", n)
	}
}

// TestDeterminismCrashAtEachStep kills the engine at every crash-hook call of
// a short run, one crash per run, then recovers and finishes. Each run must
// end with the uninterrupted run's log and state, byte for byte.
func TestDeterminismCrashAtEachStep(t *testing.T) {
	t.Parallel()
	const until sched.Time = 70_000
	cfg := testConfig(t, 13, 4)
	ref := newWorld(t, cfg)
	ref.mem = &memStore{log: ref.log}
	hooks := 0
	ref.run(until, 15, func(int) bool { hooks++; return false })
	wantLog, wantState := ref.logBytes(), ref.finalSnapshot()
	t.Logf("%d crash points", hooks)
	if hooks < 50 {
		t.Fatalf("only %d crash points", hooks)
	}
	for at := 1; at <= hooks; at += crashStride {
		t.Run(fmt.Sprintf("hook%d", at), func(t *testing.T) {
			t.Parallel()
			w := newWorld(t, cfg)
			w.mem = &memStore{log: w.log}
			if c := w.run(until, 15, func(n int) bool { return n == at }); c != 1 {
				t.Fatalf("crash at %d: %d crashes", at, c)
			}
			if !bytes.Equal(w.logBytes(), wantLog) {
				t.Fatalf("crash at hook %d of %d: log differs from the uninterrupted run", at, hooks)
			}
			if !bytes.Equal(w.finalSnapshot(), wantState) {
				t.Fatalf("crash at hook %d of %d: state differs from the uninterrupted run", at, hooks)
			}
		})
	}
}

// TestDeterminismValueConserved runs 100 wallets for two epochs and checks
// that the engine's confirmed view accounts for every lovelace of genesis,
// matches the chain, and that the chain itself conserves value.
func TestDeterminismValueConserved(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("long run")
	}
	cfg := testConfig(t, 14, 100)
	w := newWorld(t, cfg)
	until := cfg.Slots.EpochStart(2)
	w.run(until, 500, nil)
	p, err := w.start(nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer p.stop(t)
	e := p.r.Engine()
	total := genesisTotal(t, cfg)
	if e.Summary().Supply != total {
		t.Fatalf("engine accounts for %d of %d lovelace", e.Summary().Supply, total)
	}
	if got := w.chain.UnspentLovelace() + w.chain.Fees(); got != total {
		t.Fatalf("chain holds %d of %d lovelace", got, total)
	}
	if want := derive(t, cfg, w.chain); !sameBalances(want, e.Balances()) {
		t.Fatal("engine balances differ from Derive(chain)")
	}
	k := countKinds(t, w.log)
	if k[KindConfirmed] < 100 {
		t.Fatalf("only %d payments confirmed: %v", k[KindConfirmed], k)
	}
	t.Logf("2 epochs, 100 wallets: %v", k)
}

// BenchmarkFakeRun1000 runs 1000 wallets on the fake chain for one epoch and
// reports log records per second, startup included.
func BenchmarkFakeRun1000(b *testing.B) {
	cfg := testConfig(b, 15, 1000)
	var recs uint64
	for b.Loop() {
		w := newWorld(b, cfg)
		r, err := Start(context.Background(), cfg, Deps{Clock: w.clock, Log: w.log, Node: w.node, Pacer: w.node})
		if err != nil {
			b.Fatal(err)
		}
		if err := r.Run(context.Background(), cfg.Slots.EpochStart(1)); err != nil {
			b.Fatal(err)
		}
		recs += w.log.LastSeq()
	}
	b.ReportMetric(float64(recs)/b.Elapsed().Seconds(), "events/s")
}
