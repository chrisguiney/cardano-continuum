package main

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chrisguiney/cardano-continuum/internal/cli/shared/kube"
	"github.com/chrisguiney/cardano-continuum/internal/cli/shared/kubetest"
	"github.com/chrisguiney/cardano-continuum/internal/cli/shared/simflags"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/rerun"
)

// fakeRun writes a short fake-chain log and returns its path.
func fakeRun(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "run")
	args := []string{"run", "--fake", "--epochs", "1", "--out", dir, "--seed", "7", "--wallets", "6", "--epoch-slots", "60"}
	if err := run(context.Background(), args, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "events.log")
}

func TestReplayImageOnFakeLogIsANoOp(t *testing.T) {
	log := fakeRun(t)
	var out bytes.Buffer
	args := []string{"replay", "--image", "dingo=reg/dingo:x", "--seed", "7", "--wallets", "6", "--epoch-slots", "60", log}
	if err := run(context.Background(), args, &out); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "fake-chain log has no node build") {
		t.Fatalf("no explanation: %s", out.String())
	}
}

func TestReplayImageRejectsABadAssignment(t *testing.T) {
	args := []string{"replay", "--image", "nonsense", "--seed", "7", fakeRun(t)}
	if err := run(context.Background(), args, &bytes.Buffer{}); err == nil {
		t.Fatal("want an error for --image without impl=ref")
	}
}

// liveDeps returns deps whose want header is the fake log's own, or a
// different one, and a cluster that records the patches.
func liveDeps(t *testing.T, log string, same bool) (replayDeps, *kubetest.Fake) {
	t.Helper()
	l, err := eventlog.Open(log)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	h, err := rerun.HeaderOf(context.Background(), l)
	if err != nil {
		t.Fatal(err)
	}
	if !same {
		h.GenesisHash = []byte("other")
	}
	fc := kubetest.New()
	return replayDeps{
		open: func(string, string) (kube.Client, error) { return fc, nil },
		want: func(context.Context, simflags.Flags, liveFlags) (eventlog.HeaderBody, error) { return h, nil },
	}, fc
}

func TestReplayImageLiveRefusesOnMismatch(t *testing.T) {
	log := fakeRun(t)
	deps, fc := liveDeps(t, log, false)
	args := []string{"--image", "dingo=reg/dingo:x", "--live", "--network", "net", "--seed", "7", log}
	err := cmdReplayWith(context.Background(), args, &bytes.Buffer{}, deps)
	if !errors.Is(err, rerun.ErrFingerprint) {
		t.Fatalf("err = %v, want ErrFingerprint", err)
	}
	if len(fc.Applied) != 0 {
		t.Fatal("patched the network despite the mismatch")
	}
}

func TestReplayImageLiveSetsTheNetworkImage(t *testing.T) {
	log := fakeRun(t)
	deps, fc := liveDeps(t, log, true)
	var out bytes.Buffer
	args := []string{"--image", "dingo=reg/dingo:x", "--live", "--network", "net", "--seed", "7", log}
	if err := cmdReplayWith(context.Background(), args, &out, deps); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if len(fc.Applied) != 1 || fc.Applied[0].To != "reg/dingo:x" {
		t.Fatalf("applied %+v", fc.Applied)
	}
}

func TestReplayImageLiveNeedsANetwork(t *testing.T) {
	log := fakeRun(t)
	deps, _ := liveDeps(t, log, true)
	if err := cmdReplayWith(context.Background(), []string{"--image", "dingo=x", "--live", log}, &bytes.Buffer{}, deps); err == nil {
		t.Fatal("want an error without --network")
	}
}
