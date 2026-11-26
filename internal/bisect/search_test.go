package bisect

import (
	"context"
	"errors"
	"fmt"
	"math/bits"
	"slices"
	"testing"
)

func commits(n int) []string {
	c := make([]string, n)
	for i := range c {
		c[i] = fmt.Sprintf("c%03d", i)
	}
	return c
}

// fake marks commits from first on as bad; skip lists commits whose build
// fails. calls records the commits probed.
type fake struct {
	list  []string
	first int
	skip  map[int]bool
	calls []string
}

func (f *fake) Test(_ context.Context, c string) (Verdict, error) {
	f.calls = append(f.calls, c)
	var i int
	_, _ = fmt.Sscanf(c, "c%d", &i)
	switch {
	case f.skip[i]:
		return Skip, nil
	case i >= f.first:
		return Bad, nil
	}
	return Good, nil
}

func TestFirstBadInLogSteps(t *testing.T) {
	for _, n := range []int{1, 2, 7, 64, 100} {
		for _, first := range []int{0, n / 2, n - 1} {
			f := &fake{list: commits(n), first: first}
			got, err := Search(context.Background(), f.list, f)
			if err != nil {
				t.Fatalf("n=%d first=%d: %v", n, first, err)
			}
			if !got.Exact || got.First != f.list[first] {
				t.Fatalf("n=%d first=%d: got %+v", n, first, got)
			}
			// One probe verifies the bad tip, then a binary search.
			if max := 1 + bits.Len(uint(n)); len(f.calls) > max {
				t.Errorf("n=%d: %d probes, want at most %d", n, len(f.calls), max)
			}
		}
	}
}

func TestAllGoodRefusesTheBadEnd(t *testing.T) {
	f := &fake{list: commits(10), first: 99}
	if _, err := Search(context.Background(), f.list, f); !errors.Is(err, ErrBadIsGood) {
		t.Fatalf("err = %v, want ErrBadIsGood", err)
	}
}

func TestAllBadIsTheFirstCommit(t *testing.T) {
	f := &fake{list: commits(10), first: 0}
	got, err := Search(context.Background(), f.list, f)
	if err != nil || !got.Exact || got.First != "c000" {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestSkipsStepAround(t *testing.T) {
	f := &fake{list: commits(20), first: 12, skip: map[int]bool{9: true, 10: true}}
	got, err := Search(context.Background(), f.list, f)
	if err != nil || !got.Exact || got.First != "c012" {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestSkippedBoundaryIsAnAmbiguousRange(t *testing.T) {
	f := &fake{list: commits(20), first: 12, skip: map[int]bool{11: true, 12: true}}
	got, err := Search(context.Background(), f.list, f)
	if err != nil {
		t.Fatal(err)
	}
	// c012 is the first bad commit, but neither it nor its skipped
	// predecessor can be built, so the search can only name the range.
	if got.Exact || !slices.Contains(got.Candidates, "c011") || !slices.Contains(got.Candidates, "c012") {
		t.Fatalf("got %+v, want candidates holding c011 and c012", got)
	}
	if got.First != got.Candidates[len(got.Candidates)-1] {
		t.Fatalf("First %s is not the last candidate of %v", got.First, got.Candidates)
	}
}

func TestSkippedTipCannotBeVerified(t *testing.T) {
	f := &fake{list: commits(5), first: 2, skip: map[int]bool{4: true}}
	if _, err := Search(context.Background(), f.list, f); !errors.Is(err, ErrBadSkipped) {
		t.Fatalf("err = %v, want ErrBadSkipped", err)
	}
}

func TestStableAcrossReruns(t *testing.T) {
	run := func() ([]string, Result) {
		f := &fake{list: commits(37), first: 20, skip: map[int]bool{18: true}}
		r, err := Search(context.Background(), f.list, f)
		if err != nil {
			t.Fatal(err)
		}
		return f.calls, r
	}
	c1, r1 := run()
	c2, r2 := run()
	if fmt.Sprint(c1) != fmt.Sprint(c2) || fmt.Sprint(r1) != fmt.Sprint(r2) {
		t.Fatalf("runs differ: %v %v vs %v %v", c1, r1, c2, r2)
	}
}

func TestEmptyRange(t *testing.T) {
	if _, err := Search(context.Background(), nil, &fake{}); !errors.Is(err, ErrNoCommits) {
		t.Fatalf("err = %v", err)
	}
}

func TestProbeErrorStopsTheSearch(t *testing.T) {
	boom := errors.New("boom")
	tf := TestFunc(func(context.Context, string) (Verdict, error) { return Good, boom })
	if _, err := Search(context.Background(), commits(3), tf); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
}

func TestCancelledContextStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f := &fake{list: commits(4), first: 1}
	if _, err := Search(ctx, f.list, f); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}
