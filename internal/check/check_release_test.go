//go:build !debug

package check

import "testing"

// Release builds compile the checks away: a failing check must not panic and
// a closure must never run, since it may be expensive.
func TestChecksAreNoOpsInRelease(t *testing.T) {
	Pre(false, "x")
	Post(false, "x")
	PreFn(func() bool { t.Fatal("closure ran in a release build"); return false }, "x")
	PostFn(func() bool { t.Fatal("closure ran in a release build"); return false }, "x")
	if Slow {
		t.Fatal("Slow is true in a release build")
	}
}
