package sim

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chrisguiney/cardano-continuum/internal/cli/internal/kube"
	"github.com/chrisguiney/cardano-continuum/internal/cli/internal/kubetest"
	"github.com/chrisguiney/cardano-continuum/internal/cli/internal/simflags"
	"github.com/chrisguiney/cardano-continuum/internal/cli/live"
	"github.com/chrisguiney/cardano-continuum/internal/cli/rerun"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	irerun "github.com/chrisguiney/cardano-continuum/internal/rerun"
)

// fakeRun writes a short fake-chain log and returns its path.
func fakeRun(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "run")
	args := []string{"--fake", "--epochs", "1", "--out", dir, "--seed", "7", "--wallets", "6", "--epoch-slots", "60"}
	if err := Run(context.Background(), args, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "events.log")
}

func TestReplayImageOnFakeLogIsANoOp(t *testing.T) {
	t.Parallel()
	log := fakeRun(t)
	var out bytes.Buffer
	args := []string{"--image", "dingo=reg/dingo:x", "--seed", "7", "--wallets", "6", "--epoch-slots", "60", log}
	if err := Replay(context.Background(), args, &out); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "fake-chain log has no node build") {
		t.Fatalf("no explanation: %s", out.String())
	}
}

func TestReplayImageRejectsABadAssignment(t *testing.T) {
	t.Parallel()
	args := []string{"--image", "nonsense", "--seed", "7", fakeRun(t)}
	if err := Replay(context.Background(), args, &bytes.Buffer{}); err == nil {
		t.Fatal("want an error for --image without impl=ref")
	}
}

// liveDeps returns deps whose want header is the fake log's own, or a
// different one, and a cluster that records the patches.
func liveDeps(t *testing.T, log string, same bool) (rerun.Deps, *kubetest.Fake) {
	t.Helper()
	l, err := eventlog.Open(log)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	h, err := irerun.HeaderOf(context.Background(), l)
	if err != nil {
		t.Fatal(err)
	}
	if !same {
		h.GenesisHash = []byte("other")
	}
	fc := kubetest.New()
	return rerun.Deps{
		Open: func(string, string) (kube.Client, error) { return fc, nil },
		Want: func(context.Context, simflags.Flags, live.Flags) (eventlog.HeaderBody, error) { return h, nil },
	}, fc
}

func TestReplayImageLiveRefusesOnMismatch(t *testing.T) {
	t.Parallel()
	log := fakeRun(t)
	deps, fc := liveDeps(t, log, false)
	args := []string{"--image", "dingo=reg/dingo:x", "--live", "--network", "net", "--seed", "7", log}
	err := ReplayWith(context.Background(), args, &bytes.Buffer{}, deps)
	if !errors.Is(err, irerun.ErrFingerprint) {
		t.Fatalf("err = %v, want ErrFingerprint", err)
	}
	if len(fc.Applied) != 0 {
		t.Fatal("patched the network despite the mismatch")
	}
}

func TestReplayImageLiveSetsTheNetworkImage(t *testing.T) {
	t.Parallel()
	log := fakeRun(t)
	deps, fc := liveDeps(t, log, true)
	var out bytes.Buffer
	args := []string{"--image", "dingo=reg/dingo:x", "--live", "--network", "net", "--seed", "7", log}
	if err := ReplayWith(context.Background(), args, &out, deps); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if len(fc.Applied) != 1 || fc.Applied[0].To != "reg/dingo:x" {
		t.Fatalf("applied %+v", fc.Applied)
	}
}

func TestReplayImageLiveNeedsANetwork(t *testing.T) {
	t.Parallel()
	log := fakeRun(t)
	deps, _ := liveDeps(t, log, true)
	if err := ReplayWith(context.Background(), []string{"--image", "dingo=x", "--live", log}, &bytes.Buffer{}, deps); err == nil {
		t.Fatal("want an error without --network")
	}
}
