package oracle

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
	s := NewSet()
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.Add(wrongAccept(uint64(i), "p1"))
			_, _ = s.Page(0, 10)
		}()
	}
	wg.Wait()
	if s.Len() != 1 {
		t.Fatalf("len = %d", s.Len())
	}
}

func TestRecordRoundTrip(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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

func TestReplayCommand(t *testing.T) {
	t.Parallel()
	r := Run{Seed: 3, Log: "my run/it's.log", Flags: []string{"--k", "5"}}
	if got, want := r.Command(), `continuum replay --seed 3 --k 5 'my run/it'\''s.log'`; got != want {
		t.Fatalf("command = %s, want %s", got, want)
	}
	if (Run{Seed: 3}).Command() != "" {
		t.Fatal("a run without a log has a replay command")
	}
}
