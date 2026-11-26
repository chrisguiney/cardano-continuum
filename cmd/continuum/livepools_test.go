package main

import (
	"context"
	"log/slog"
	"testing"

	"github.com/chrisguiney/cardano-continuum/api/v1alpha1"
	"github.com/chrisguiney/cardano-continuum/internal/engine"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/httpapi"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

type fakePools struct {
	pools   []v1alpha1.ContinuumPool
	patches map[string]map[string]any
}

func (f *fakePools) Pools(context.Context, string) ([]v1alpha1.ContinuumPool, error) {
	return f.pools, nil
}

func (f *fakePools) PatchPool(_ context.Context, name string, spec map[string]any) error {
	f.patches[name] = spec
	return nil
}

func pool(name, id string, phase v1alpha1.PoolPhase) v1alpha1.ContinuumPool {
	p := v1alpha1.ContinuumPool{Spec: v1alpha1.ContinuumPoolSpec{PoolID: id}, Status: v1alpha1.ContinuumPoolStatus{Phase: phase}}
	p.Name = name
	return p
}

// TestLivePoolsPatchesThePlan: only genesis pools whose spec differs from
// the plan are patched, by poolId; and pod health changes are reported once.
func TestLivePoolsPatchesThePlan(t *testing.T) {
	f := &fakePools{patches: map[string]map[string]any{}, pools: []v1alpha1.ContinuumPool{
		pool("p0", "continuum-pool-0", v1alpha1.PhaseSyncing), pool("p1", "continuum-pool-1", v1alpha1.PhaseSyncing),
	}}
	lp := newLivePools(f, "devnet", "continuum-pool-%d", map[int]uint64{0: 1, 1: 2}, slog.New(slog.DiscardHandler))
	plan := []engine.PoolPlan{{Actor: 1, Genesis: 0}, {Actor: 2, Genesis: 1, Dark: true, Image: "img"}, {Actor: 3, Genesis: -1, Dark: true}}
	if err := lp.Sync(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	if len(f.patches) != 1 || f.patches["p1"]["dark"] != true || f.patches["p1"]["image"] != "img" {
		t.Fatalf("patches %v", f.patches)
	}
	if got, _ := lp.Changes(context.Background(), 0); len(got) != 0 {
		t.Fatalf("running pods reported %v", got)
	}
	f.pools[1].Status.Phase = v1alpha1.PhaseDark
	if got, _ := lp.Changes(context.Background(), 5_000); len(got) != 0 {
		t.Fatalf("polled before the interval: %v", got)
	}
	got, _ := lp.Changes(context.Background(), 10_000)
	if len(got) != 1 || got[0] != (engine.PoolStatus{Pool: 2}) {
		t.Fatalf("changes %v, want pool actor 2 stopped", got)
	}
	if got, _ := lp.Changes(context.Background(), 20_000); len(got) != 0 {
		t.Fatalf("an unchanged pod reported again: %v", got)
	}
}

// TestControlQueueIsIdempotent: a request id is queued once and the loop
// drains each control once.
func TestControlQueueIsIdempotent(t *testing.T) {
	q := newControlQueue()
	c := httpapi.Control{RequestID: "r", Op: httpapi.OpPause}
	for i, dup := range []bool{false, true} {
		r, err := q.Submit(context.Background(), c)
		if err != nil || r.Duplicate != dup {
			t.Fatalf("submit %d: %+v %v", i, r, err)
		}
	}
	bs, _ := q.Controls(context.Background())
	if len(bs) != 1 {
		t.Fatalf("%d controls drained", len(bs))
	}
	var got httpapi.Control
	if err := eventlog.Decode(bs[0], &got); err != nil || got.Op != httpapi.OpPause {
		t.Fatalf("control %+v %v", got, err)
	}
	if bs, _ := q.Controls(context.Background()); len(bs) != 0 {
		t.Fatal("a control was drained twice")
	}
}

// TestFeedLogNumbersRecords: records appended through the live API's log
// reach its feed with the seqs the log gave them.
func TestFeedLogNumbersRecords(t *testing.T) {
	mem := &eventlog.MemLog{}
	s, err := newLiveServer(context.Background(), mem, nil, testSlots(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.hub.Close()
	body, _ := eventlog.Encode(engine.Spawned{Profile: "holder"})
	rec := eventlog.Record{Class: eventlog.Decision, Kind: engine.KindSpawned, Actor: 1, Body: body}
	if err := s.log.Append(context.Background(), rec, rec); err != nil {
		t.Fatal(err)
	}
	snap, err := s.src.Snapshot(context.Background())
	if err != nil || snap.LastSeq != 2 {
		t.Fatalf("snapshot %+v %v, want last seq 2", snap, err)
	}
}

func testSlots(t *testing.T) sched.Slots {
	t.Helper()
	s, err := sched.NewSlots(1000, 60)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
