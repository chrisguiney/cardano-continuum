// Package bisect finds the first commit of a dependency that makes a check
// fail, the way git bisect does. The search is pure: it walks a commit list
// and asks a Test for each verdict, so builds and predicates plug in from
// outside and the result depends only on the list and the verdicts.
package bisect

import (
	"context"
	"errors"
	"fmt"
)

// Verdict is what a Test says about one commit.
type Verdict int

// The verdicts a Test returns.
const (
	// Good: the check passes, the fault is absent.
	Good Verdict = iota
	// Bad: the check fails, the fault is present.
	Bad
	// Skip: the commit cannot be judged, for instance its build fails.
	Skip
)

func (v Verdict) String() string {
	switch v {
	case Good:
		return "good"
	case Bad:
		return "bad"
	}
	return "skip"
}

// Search errors.
var (
	// ErrNoCommits means the range holds no commit to test.
	ErrNoCommits = errors.New("bisect: no commits between good and bad")
	// ErrBadIsGood means the bad end of the range passes the check, so
	// there is no fault to find in it.
	ErrBadIsGood = errors.New("bisect: the bad end of the range passes the check")
	// ErrBadSkipped means the bad end cannot be judged, so the search has
	// nothing to anchor on.
	ErrBadSkipped = errors.New("bisect: the bad end of the range cannot be judged")
)

// Test judges one commit.
type Test interface {
	Test(ctx context.Context, commit string) (Verdict, error)
}

// TestFunc adapts a function to Test.
type TestFunc func(ctx context.Context, commit string) (Verdict, error)

// Test implements Test.
func (f TestFunc) Test(ctx context.Context, commit string) (Verdict, error) { return f(ctx, commit) }

// Result is the outcome of a search.
type Result struct {
	// First is the first bad commit when Exact; otherwise the earliest
	// commit known to be bad.
	First string
	// Exact is false when skipped commits leave the first bad commit
	// somewhere in Candidates.
	Exact bool
	// Candidates are the commits, oldest first, that may be the first bad
	// one. Empty when Exact.
	Candidates []string
	// Probes counts the commits tested.
	Probes int
}

// Search finds the first bad commit in commits, oldest first. The commit
// before commits[0] is taken as good and commits[len-1] is verified bad
// first, so a range that does not show the fault fails fast instead of
// returning its last commit. The probe order depends only on commits and
// the verdicts, so a rerun with the same verdicts repeats it.
//
// It takes at most 1 + ceil(log2(n)) probes when nothing is skipped. A skip
// is stepped around by probing the nearest untested commit; when every
// commit left in the window is skipped the result names the window.
func Search(ctx context.Context, commits []string, t Test) (Result, error) {
	n := len(commits)
	if n == 0 {
		return Result{}, ErrNoCommits
	}
	probes := 0
	probe := func(i int) (Verdict, error) {
		if err := ctx.Err(); err != nil {
			return Skip, err
		}
		probes++
		v, err := t.Test(ctx, commits[i])
		if err != nil {
			return Skip, fmt.Errorf("test %s: %w", commits[i], err)
		}
		return v, nil
	}
	switch v, err := probe(n - 1); {
	case err != nil:
		return Result{}, err
	case v == Good:
		return Result{}, ErrBadIsGood
	case v == Skip:
		return Result{}, ErrBadSkipped
	}
	lo, hi := 0, n-1 // the first bad commit is in [lo, hi]; hi is known bad
	skipped := map[int]bool{}
	for lo < hi {
		i, ok := pick(lo, hi, skipped)
		if !ok {
			return Result{First: commits[hi], Candidates: append([]string(nil), commits[lo:hi+1]...), Probes: probes}, nil
		}
		v, err := probe(i)
		if err != nil {
			return Result{}, err
		}
		switch v {
		case Good:
			lo = i + 1
		case Bad:
			hi = i
		default:
			skipped[i] = true
		}
	}
	return Result{First: commits[hi], Exact: true, Probes: probes}, nil
}

// pick returns the untested commit in [lo, hi) nearest the middle, trying
// the lower neighbour first on a tie.
func pick(lo, hi int, skipped map[int]bool) (int, bool) {
	mid := (lo + hi) / 2
	for d := 0; d <= hi-lo; d++ {
		for _, i := range [2]int{mid - d, mid + d} {
			if i >= lo && i < hi && !skipped[i] {
				return i, true
			}
		}
	}
	return 0, false
}
