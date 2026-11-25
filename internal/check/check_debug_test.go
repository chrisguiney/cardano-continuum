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

func TestPreFnRunsInDebug(t *testing.T) {
	ran := false
	PreFn(func() bool { ran = true; return true }, "x")
	if !ran {
		t.Fatal("PreFn did not run its check in a debug build")
	}
}

func TestPostFnFailsInDebug(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("PostFn(false) did not panic in a debug build")
		}
	}()
	PostFn(func() bool { return false }, "x")
}

func TestSlowIsTrueInDebug(t *testing.T) {
	if !Slow {
		t.Fatal("Slow is false in a debug build")
	}
}
