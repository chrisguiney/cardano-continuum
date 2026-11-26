package httpapi

import (
	"context"
	"errors"
	"testing"
	"time"

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
