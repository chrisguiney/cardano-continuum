package engine

import (
	"bytes"
	"errors"
	"slices"
	"testing"

	"github.com/chrisguiney/cardano-continuum/internal/build"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/protocol"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

// forkProposedAt are the times of the run's hard-fork proposals.
func forkProposedAt(t *testing.T, w *world) []sched.Time {
	t.Helper()
	var out []sched.Time
	for _, rec := range logRecords(t, w.log) {
		var s Submitted
		if rec.Kind == KindSubmitted && eventlog.Decode(rec.Body, &s) == nil && s.Action == string(build.ActionHardFork) {
			out = append(out, rec.At)
		}
	}
	return out
}

// A run with a schedule takes the devnet from 10.0 to 11.0 with no inject:
// the network proposer proposes at the first epoch boundary at or after the
// scheduled epoch where governance is ready, the voters ratify and the next
// boundary enacts. Nothing proposes before the scheduled epoch.
func TestScheduledHardForkNeedsNoInject(t *testing.T) {
	t.Parallel()
	cfg := populationConfig(t, 4, populationFile(t, govYAML))
	cfg.Committee = 3
	cfg.HardFork = protocol.Schedule{To: protocol.Version{Major: 11}, Epoch: 2}
	w := newWorld(t, cfg)
	w.run(12*3600*1000, 50, nil)

	p, err := w.start(nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	v := p.r.Engine().win.View()
	want := protocol.Version{Major: 11}
	if got := v.VersionAt(v.Epoch()); got != want {
		t.Fatalf("view at %v in epoch %d, want %v (forks %+v)", got, v.Epoch(), want, v.Forks())
	}

	if pv := w.chain.Params().ProtocolVersion; pv.Major != 11 || pv.Minor != 0 {
		t.Fatalf("fake chain at %d.%d, want 11.0", pv.Major, pv.Minor)
	}

	at := forkProposedAt(t, w)
	if len(at) == 0 || at[0] < cfg.Slots.EpochStart(2) {
		t.Fatalf("hard fork proposed at %v, want one, none before epoch 2 (%d)", at, cfg.Slots.EpochStart(2))
	}

	if by := forkProposers(t, w, p.r.Engine()); slices.ContainsFunc(by, func(n string) bool { return n != proposerName }) {
		t.Fatalf("proposed by profiles %v, want only %q", by, proposerName)
	}

	recs := protocolRecords(t, w.log)
	plan := &protocol.Schedule{To: want, Epoch: 2}
	if len(recs) < 3 || recs[0].Plan == nil || *recs[0].Plan != *plan || recs[len(recs)-1].Plan != nil {
		t.Fatalf("protocol records %+v: want the schedule on them until the fork is enacted", recs)
	}

	if fs := findings(t, w.log); len(fs) > 0 {
		t.Fatalf("%d findings, the first %+v", len(fs), fs[0])
	}
}

// With no schedule the devnet stays at 10.0 and nothing proposes a fork.
func TestNoScheduleProposesNoHardFork(t *testing.T) {
	t.Parallel()
	cfg := populationConfig(t, 4, populationFile(t, govYAML))
	cfg.Committee = 3
	w := newWorld(t, cfg)
	w.run(12*3600*1000, 50, nil)

	if at := forkProposedAt(t, w); len(at) != 0 {
		t.Fatalf("hard fork proposed at %v with no schedule", at)
	}

	if pv := w.chain.Params().ProtocolVersion; pv.Major != 10 {
		t.Fatalf("fake chain at %d.%d, want 10.x", pv.Major, pv.Minor)
	}

	for _, r := range protocolRecords(t, w.log) {
		if r.Plan != nil {
			t.Fatalf("protocol record %+v carries a plan with no schedule", r)
		}
	}
}

// A crash and restore mid-run changes nothing about a scheduled fork: the
// restored engine arms the next epoch boundary from its clock, so the log
// and the final state are the uncrashed run's.
func TestDeterminismScheduledHardFork(t *testing.T) {
	t.Parallel()
	cfg := populationConfig(t, 4, populationFile(t, govYAML))
	cfg.Committee = 3
	cfg.HardFork = protocol.Schedule{To: protocol.Version{Major: 11}, Epoch: 2}
	until := sched.Time(8 * 3600 * 1000)

	ref := newWorld(t, cfg)
	ref.mem = &memStore{log: ref.log}
	hooks := 0
	ref.run(until, 15, func(int) bool { hooks++; return false })
	want := ref.logBytes()

	w := newWorld(t, cfg)
	w.mem = &memStore{log: w.log}
	if c := w.run(until, 15, func(n int) bool { return n == hooks/2 }); c != 1 {
		t.Fatalf("%d crashes", c)
	}

	if !bytes.Equal(w.logBytes(), want) {
		t.Fatal("a crash mid-run changes a scheduled hard fork's log")
	}

	if !bytes.Equal(w.finalSnapshot(), ref.finalSnapshot()) {
		t.Fatal("a crash mid-run changes a scheduled hard fork's state")
	}
}

// The schedule is part of the run's identity: a log written under one
// schedule is not replayed under another, and no schedule hashes as a
// config that never had the field.
func TestHardForkIsInTheHeader(t *testing.T) {
	t.Parallel()
	hash := func(s protocol.Schedule) []byte {
		cfg := testConfig(t, 7, 6)
		cfg.HardFork = s
		h, err := cfg.Header()
		if err != nil {
			t.Fatal(err)
		}
		return h.GenesisHash
	}

	none, a := hash(protocol.Schedule{}), hash(protocol.Schedule{To: protocol.Version{Major: 11}, Epoch: 3})
	b := hash(protocol.Schedule{To: protocol.Version{Major: 11}, Epoch: 4})
	if bytes.Equal(none, a) || bytes.Equal(a, b) {
		t.Fatal("the schedule does not change the genesis hash")
	}
}

// A schedule the genesis version cannot reach by one hard fork is refused
// when the engine is built, not retried every epoch.
func TestHardForkScheduleMustFollowGenesis(t *testing.T) {
	t.Parallel()
	cfg := populationConfig(t, 4, populationFile(t, govYAML))
	cfg.HardFork = protocol.Schedule{To: protocol.Version{Major: 12}, Epoch: 2}
	if _, err := New(cfg); !errors.Is(err, ErrConfig) {
		t.Fatalf("New with a schedule to 12.0 from %v: %v, want ErrConfig", cfg.Build.Protocol, err)
	}
}
