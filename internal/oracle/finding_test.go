package oracle

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/httpapi"
	"github.com/chrisguiney/cardano-continuum/internal/mistake"
	"github.com/chrisguiney/cardano-continuum/internal/submit"
)

func wrongAccept(seq uint64, pool string) Finding {
	a := attempt(expect(mistake.Of(mistake.FeeTooSmall), false),
		Answer{Node: node("dingo", pool), Outcome: accepted()})
	a.Seq = seq
	return Judge(a, run)[0]
}

// The ID is a hash of the kind and the identifying evidence only: a replay,
// a resend or another pool of the same implementation giving the same wrong
// answer is the same finding, while a different answer is not.
func TestFindingStableID(t *testing.T) {
	a, b := wrongAccept(42, "p1"), wrongAccept(99, "p2")
	if a.ID != b.ID {
		t.Fatalf("same wrong answer, different ids %s %s", a.ID, b.ID)
	}
	if len(a.ID) != 32 {
		t.Fatalf("id %q is not 128 bits of hex", a.ID)
	}
	again := attempt(expect(mistake.Of(mistake.FeeTooSmall), false),
		Answer{Node: node("dingo", "p1"), Outcome: rejected(submit.ValueNotConserved)})
	if c := Judge(again, run)[0]; c.ID == a.ID {
		t.Fatal("a different answer shares the id")
	}
	v := value()
	v.Fees++
	s := Snapshot{Value: v, Run: run}
	m := Monitors(k, guard(t))[0]
	s.Seq = 1
	x := m.Check(s)
	s.Seq = 2
	y := m.Check(s)
	if x[0].ID != y[0].ID || x[0].ID == a.ID {
		t.Fatalf("monitor ids %s %s", x[0].ID, y[0].ID)
	}
	// Pinned: changing the hash input breaks dedup across logs.
	if a.ID != "371cfdfc5e557a3e614f640a0b959df8" {
		t.Fatalf("id = %s: the hash input changed", a.ID)
	}
}

func TestSetDedups(t *testing.T) {
	s := NewSet()
	a, b := wrongAccept(42, "p1"), wrongAccept(99, "p2")
	v := value()
	v.Fees++
	c := Monitors(k, guard(t))[0].Check(Snapshot{Value: v, Seq: 50, Run: run})[0]
	if got := s.Add(a, b, c, a); len(got) != 2 || got[0].ID != a.ID || got[1].ID != c.ID {
		t.Fatalf("new = %+v", got)
	}
	if s.Len() != 2 {
		t.Fatalf("len = %d", s.Len())
	}
	if f, ok := s.Find(a.ID); !ok || f.Seq != 42 {
		t.Fatalf("first occurrence not kept: %+v %v", f, ok)
	}
}

func TestSetConcurrent(t *testing.T) {
	s := NewSet()
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.Add(wrongAccept(uint64(i), "p1"))
			_, _ = s.Findings(context.Background(), httpapi.Page{Limit: 10})
		}()
	}
	wg.Wait()
	if s.Len() != 1 {
		t.Fatalf("len = %d", s.Len())
	}
}

func TestRecordRoundTrip(t *testing.T) {
	dirty := node("dingo", "p2")
	dirty.Dirty = true
	f := wrongAccept(42, "p1")
	f.Nodes = append(f.Nodes, dirty)
	r, err := f.Record(1234)
	if err != nil {
		t.Fatal(err)
	}
	if r.Class != eventlog.Decision || r.Kind != KindFinding || r.At != 1234 {
		t.Fatalf("record %+v", r)
	}
	frame, err := eventlog.AppendFrame(nil, r)
	if err != nil {
		t.Fatal(err)
	}
	back, _, err := eventlog.DecodeFrame(frame)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Decode(back)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, f) {
		t.Fatalf("round trip:\n got %+v\nwant %+v", got, f)
	}
	other := back
	other.Kind = eventlog.KindTick
	if _, err := Decode(other); !errors.Is(err, ErrNotFinding) {
		t.Fatalf("decode of another kind: %v", err)
	}
	// Encoding is canonical: equal findings give equal bytes, so replay
	// compares them as decisions.
	r2, err := f.Record(1234)
	if err != nil || string(r2.Body) != string(r.Body) {
		t.Fatalf("encoding is not deterministic: %v", err)
	}
}

func TestSetLoadsFromLog(t *testing.T) {
	var log eventlog.MemLog
	ctx := context.Background()
	a := wrongAccept(42, "p1")
	r, err := a.Record(5)
	if err != nil {
		t.Fatal(err)
	}
	if err := log.Append(ctx, eventlog.Record{Class: eventlog.Input, Kind: eventlog.KindTick}, r, r); err != nil {
		t.Fatal(err)
	}
	s := NewSet()
	if err := log.Scan(ctx, 1, s.Load); err != nil {
		t.Fatal(err)
	}
	if s.Len() != 1 {
		t.Fatalf("len = %d", s.Len())
	}
	if got := s.Add(a); len(got) != 0 {
		t.Fatal("a finding already in the log was new again")
	}
}

// The adapter serialises to the httpapi Finding shape: the expected verdict
// is one of the sources, and every node answer carries its build.
func TestFindingsPageGolden(t *testing.T) {
	s := NewSet()
	dirty := node("dingo", "p2")
	dirty.Dirty = true
	dirty.Revisions = map[string]string{"dingo": "abc123", "gouroboros": "def456"}
	a := attempt(expect(mistake.Of(mistake.FeeTooSmall), false),
		Answer{Node: node("cardano-node", "p1"), Outcome: rejected(submit.FeeTooSmall)},
		Answer{Node: dirty, Outcome: accepted()},
	)
	s.Add(Judge(a, run)...)
	split, _ := Differential(a, run)
	v := value()
	v.Reserves += 3
	s.Add(split)
	s.Add(Monitors(k, guard(t))[0].Check(Snapshot{Value: v, Seq: 50, Run: run})...)

	page, err := s.Findings(context.Background(), httpapi.Page{Limit: 2, Offset: 1})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 3 || len(page.Rows) != 2 {
		t.Fatalf("page = %+v", page)
	}
	got, err := json.MarshalIndent(page, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	golden := filepath.Join("testdata", "findings.golden.json")
	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.WriteFile(golden, append(got, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if string(want) != string(got)+"\n" {
		t.Fatalf("page differs from %s:\n%s", golden, got)
	}
	empty, err := NewSet().Findings(context.Background(), httpapi.Page{Limit: 10})
	if err != nil || empty.Rows == nil || empty.Total != 0 {
		t.Fatalf("empty page %+v %v: rows must serialise as []", empty, err)
	}
	if _, err := s.Findings(context.Background(), httpapi.Page{Limit: 1, Offset: 10}); err != nil {
		t.Fatalf("offset past the end: %v", err)
	}
}

func TestReplayCommand(t *testing.T) {
	r := Run{Seed: 3, Log: "my run/it's.log", Flags: []string{"--k", "5"}}
	if got, want := r.Command(), `continuum replay --seed 3 --k 5 'my run/it'\''s.log'`; got != want {
		t.Fatalf("command = %s, want %s", got, want)
	}
	if (Run{Seed: 3}).Command() != "" {
		t.Fatal("a run without a log has a replay command")
	}
}
