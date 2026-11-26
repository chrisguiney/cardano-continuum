package tla

import (
	"context"
	"testing"
	"time"
)

// TestTLCFlags settles which trace-dump flag the pinned TLC accepts: the run
// must produce a parseable JSON counterexample.
func TestTLCFlags(t *testing.T) {
	tools := testTools(t)
	run, err := RunTLC(context.Background(), tools, helloDir(t), "Hello.tla", "HelloBug.cfg", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if run.Outcome != Violation || run.Invariant != "NeverDoneAfterPong" {
		t.Fatalf("outcome %v invariant %q\n%s", run.Outcome, run.Invariant, run.Output)
	}
	if run.Version == "" {
		t.Error("no TLC version")
	}
	if run.Trace == nil || len(run.Trace.Steps) != 4 {
		t.Fatalf("trace = %+v", run.Trace)
	}
}

func TestRunTLCPass(t *testing.T) {
	tools := testTools(t)
	run, err := RunTLC(context.Background(), tools, helloDir(t), "Hello.tla", "Hello.cfg", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if run.Outcome != Passed || run.Trace != nil {
		t.Fatalf("outcome %v trace %v", run.Outcome, run.Trace)
	}
}

func TestRunTLCTimeout(t *testing.T) {
	tools := testTools(t)
	run, err := RunTLC(context.Background(), tools, helloDir(t), "Hello.tla", "HelloBug.cfg", time.Nanosecond)
	if err != nil {
		t.Fatal(err)
	}
	if run.Outcome != TimedOut {
		t.Fatalf("outcome %v", run.Outcome)
	}
}
