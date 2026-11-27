package sim

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// TestFakeRunMistakesFlag: mistakes are on by default for the shipped
// profiles, whose competence falls below 1; the summary reports them, and
// the replay command line must agree with the run's.
func TestFakeRunMistakesFlag(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "run")
	flags := []string{"--seed", "4", "--wallets", "30", "--epoch-slots", "120"}
	var out bytes.Buffer
	if err := Run(ctx, append([]string{"--fake", "--epochs", "2", "--out", dir}, flags...), &out); err != nil {
		t.Fatalf("run: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "mistakes true: rejections ") || !strings.Contains(out.String(), "findings 0 (fatal 0)") {
		t.Fatalf("summary: %s", out.String())
	}
	log := filepath.Join(dir, "events.log")
	out.Reset()
	if err := Replay(ctx, append(append([]string(nil), flags...), log), &out); err != nil || !strings.Contains(out.String(), ": 0 mismatches") {
		t.Fatalf("replay: %v\n%s", err, out.String())
	}
	if err := Replay(ctx, append(append([]string{"--mistakes", "off"}, flags...), log), &out); err == nil {
		t.Fatal("replay with mistakes off of a run with them on succeeded")
	}
	if err := Run(ctx, []string{"--fake", "--out", t.TempDir(), "--mistakes", "maybe"}, &out); err == nil {
		t.Fatal("--mistakes maybe accepted")
	}
}
