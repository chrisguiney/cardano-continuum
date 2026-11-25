//go:build !debug

package check

import "testing"

// Release builds compile the checks away: a failing check must not panic and
// a Slow closure must never run, since it may be expensive.
func TestChecksAreNoOpsInRelease(t *testing.T) {
	Pre(false, "x")
	Post(false, "x")
	Slow("x", func() bool { t.Fatal("Slow closure ran in a release build"); return false })
}
