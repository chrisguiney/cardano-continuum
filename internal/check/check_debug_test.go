//go:build debug

package check

import "testing"

func TestPreFailsInDebug(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("Pre(false) did not panic in a debug build")
		}
	}()
	Pre(false, "x")
}

func TestPrePassesInDebug(t *testing.T) {
	Pre(true, "x")
	Post(true, "x")
}

func TestSlowRunsInDebug(t *testing.T) {
	ran := false
	Slow("x", func() bool { ran = true; return true })
	if !ran {
		t.Fatal("Slow did not run its check in a debug build")
	}
}

func TestSlowFailsInDebug(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("Slow(false) did not panic in a debug build")
		}
	}()
	Slow("x", func() bool { return false })
}
