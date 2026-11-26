package httpapi

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/chrisguiney/cardano-continuum/api/v1alpha1"
	"github.com/chrisguiney/cardano-continuum/internal/engine"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/oracle"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
	"github.com/chrisguiney/cardano-continuum/internal/store"
)

type fakeNames struct{ rows []store.ActorRow }

func (f fakeNames) Actors(_ context.Context, q store.ActorQuery) ([]store.ActorRow, error) {
	if q.Offset >= len(f.rows) {
		return nil, nil
	}
	end := min(q.Offset+q.Limit, len(f.rows))
	return f.rows[q.Offset:end], nil
}

func TestLogSourceActorsPagesSortsFiltersAndNames(t *testing.T) {
	path := writeLog(t, fixture(t))
	tl, p, h := newTail(t, path)
	if _, err := tl.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	names := fakeNames{rows: []store.ActorRow{{ID: 1, Name: "alice", Role: "wallet"}, {ID: 2, Name: "bob", Role: "pool"}}}
	src := NewLogSource(LogSourceConfig{Projection: p, Hub: h, Names: names, Now: time.Now})
	ctx := context.Background()

	pg, err := src.Actors(ctx, ActorQuery{Sort: "name", Desc: true, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if pg.Total != 2 || pg.Rows[0].Name != "bob" || pg.Rows[0].Role != "pool" || pg.Rows[1].Name != "alice" {
		t.Fatalf("page %+v", pg)
	}
	pg, _ = src.Actors(ctx, ActorQuery{Sort: "txs", Desc: true, Limit: 1})
	if pg.Rows[0].ID != 1 || pg.Rows[0].Txs != 2 || pg.Rows[0].Retries != 1 || pg.Total != 2 {
		t.Fatalf("by txs %+v", pg)
	}
	pg, _ = src.Actors(ctx, ActorQuery{Sort: "id", Offset: 1, Limit: 5})
	if len(pg.Rows) != 1 || pg.Rows[0].ID != 2 {
		t.Fatalf("offset %+v", pg)
	}
	pg, _ = src.Actors(ctx, ActorQuery{Sort: "id", Limit: 5, Filter: ActorFilter{Profile: "yield"}})
	if pg.Total != 1 || pg.Rows[0].ID != 2 {
		t.Fatalf("filter %+v", pg)
	}
	pg, _ = src.Actors(ctx, ActorQuery{Sort: "id", Limit: 5, Filter: ActorFilter{Role: "pool"}})
	if pg.Total != 1 || pg.Rows[0].ID != 2 {
		t.Fatalf("role filter %+v", pg)
	}
	if _, err := src.Actors(ctx, ActorQuery{Sort: "nope", Limit: 5}); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("bad sort: %v", err)
	}
	if _, err := src.Actors(ctx, ActorQuery{Sort: "id", Limit: MaxLimit + 1}); !errors.Is(err, ErrBadRequest) {
		t.Fatalf("big limit: %v", err)
	}
}

func TestLogSourceSnapshotEpochAndPoolsWithoutKube(t *testing.T) {
	path := writeLog(t, fixture(t))
	tl, p, h := newTail(t, path)
	if _, err := tl.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	slots, err := sched.NewSlots(1000, 10)
	if err != nil {
		t.Fatal(err)
	}
	src := NewLogSource(LogSourceConfig{Projection: p, Hub: h, Slots: &slots, Kube: fakeKube{mode: "mesh"}})
	s, err := src.Snapshot(context.Background())
	if err != nil || s.Epoch == nil || *s.Epoch != 2 {
		t.Fatalf("epoch %v %v", s.Epoch, err)
	}
	if s.TxPerSec != 1.0/20 {
		t.Fatalf("tx/s %v", s.TxPerSec)
	}
	v, err := src.Pools(context.Background())
	if err != nil || v.Pools == nil || v.Topology.Edges == nil || v.Forks == nil {
		t.Fatalf("empty view must hold empty slices, not null: %+v %v", v, err)
	}
	f, err := src.Findings(context.Background(), Page{Limit: 10})
	if err != nil || f.Total != 0 || f.Rows == nil {
		t.Fatalf("findings %+v %v", f, err)
	}
}

// Findings in the log are served once each, in order of first occurrence;
// a configured set (a live engine's) is served instead.
func TestLogSourceFindingsFromLog(t *testing.T) {
	f := oracle.Finding{ID: "f1", Kind: oracle.ValueNotConserved, Severity: oracle.Fatal, Seq: 9, Summary: "off by 7"}
	rec, err := f.Record(100)
	if err != nil {
		t.Fatal(err)
	}
	tl, p, h := newTail(t, writeLog(t, append(fixture(t), rec, rec)))
	if _, err := tl.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	page, err := NewLogSource(LogSourceConfig{Projection: p, Hub: h}).Findings(context.Background(), Page{Limit: 10})
	if err != nil || page.Total != 1 || len(page.Rows) != 1 || page.Rows[0].ID != "f1" || page.Rows[0].Summary != "fatal value_not_conserved: off by 7" {
		t.Fatalf("findings %+v %v", page, err)
	}
	live := oracle.NewSet()
	live.Add(oracle.Finding{ID: "f2", Kind: oracle.PaidTwice, Severity: oracle.Fatal})
	page, err = NewLogSource(LogSourceConfig{Projection: p, Hub: h, Findings: live}).Findings(context.Background(), Page{Limit: 10})
	if err != nil || page.Total != 1 || page.Rows[0].ID != "f2" {
		t.Fatalf("live findings %+v %v", page, err)
	}
}

// A genesis actor enters the log through its first transition, never a
// Spawned record, so only the store knows its profile. Every actor must
// still be counted under some profile.
func TestLogSourceProfileCountsSumToActors(t *testing.T) {
	recs := fixture(t)
	for id := uint64(3); id <= 5; id++ {
		recs = append(recs, eventlog.Record{At: 100, Class: eventlog.Decision, Kind: engine.KindTransition, Actor: eventlog.ActorID(id), Body: enc(t, engine.Transition{From: "new", To: "active"})})
	}
	tl, p, h := newTail(t, writeLog(t, recs))
	if _, err := tl.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	names := fakeNames{rows: []store.ActorRow{
		{ID: 1, Name: "hodl-1", Role: "active", Profile: "hodl"}, {ID: 2, Name: "yield-2", Role: "new", Profile: "yield"},
		{ID: 3, Name: "yield-3", Role: "active", Profile: "yield"}, {ID: 4, Name: "yield-4", Role: "active", Profile: "yield"},
		{ID: 5, Name: "hodl-5", Role: "active", Profile: "hodl"},
	}}
	src := NewLogSource(LogSourceConfig{Projection: p, Hub: h, Names: names, Now: time.Now})
	s, err := src.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	sum := 0
	for _, n := range s.Population.ByProfile {
		sum += n
	}
	if s.Population.Total != 5 || sum != s.Population.Total {
		t.Fatalf("total %d, by profile %v sums to %d", s.Population.Total, s.Population.ByProfile, sum)
	}
	if s.Population.ByProfile["yield"] != 3 || s.Population.ByProfile["hodl"] != 2 {
		t.Fatalf("by profile %v", s.Population.ByProfile)
	}
	pg, err := src.Actors(context.Background(), ActorQuery{Sort: "id", Limit: 10, Filter: ActorFilter{Profile: "yield"}})
	if err != nil || pg.Total != 3 || pg.Rows[2].ID != 4 || pg.Rows[2].Profile != "yield" {
		t.Fatalf("profile filter over genesis actors: %+v %v", pg, err)
	}
}

type fakeKube struct {
	pools []v1alpha1.ContinuumPool
	mode  v1alpha1.TopologyMode
}

func (f fakeKube) Pools(context.Context) ([]v1alpha1.ContinuumPool, error) { return f.pools, nil }
func (f fakeKube) Topology(context.Context) (v1alpha1.TopologyMode, error) { return f.mode, nil }
