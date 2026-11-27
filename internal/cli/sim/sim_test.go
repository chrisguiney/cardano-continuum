package sim

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chrisguiney/cardano-continuum/internal/engine"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/store"
)

// TestDeterminismCLIRuns is `make determinism`'s double run: two fake runs
// with one seed write byte-identical logs, replay reproduces them, and a
// replay under another seed is refused.
func TestDeterminismCLIRuns(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	flags := []string{"--seed", "3", "--wallets", "30", "--epoch-slots", "120"}
	var logs [][]byte
	for _, name := range []string{"a", "b"} {
		var out bytes.Buffer
		args := append([]string{"run", "--fake", "--epochs", "2", "--out", filepath.Join(dir, name)}, flags...)
		if err := run(ctx, args, &out); err != nil {
			t.Fatalf("run: %v\n%s", err, out.String())
		}
		if !strings.Contains(out.String(), "value conserved: true") {
			t.Fatalf("run summary: %s", out.String())
		}
		b, err := os.ReadFile(filepath.Join(dir, name, "events.log"))
		if err != nil {
			t.Fatal(err)
		}
		logs = append(logs, b)
	}
	if !bytes.Equal(logs[0], logs[1]) {
		t.Fatal("same-seed runs wrote different logs")
	}
	var out bytes.Buffer
	logPath := filepath.Join(dir, "a", "events.log")
	if err := run(ctx, append(append([]string{"replay"}, flags...), logPath), &out); err != nil {
		t.Fatalf("replay: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), ": 0 mismatches") {
		t.Fatalf("replay output: %s", out.String())
	}
	if err := run(ctx, []string{"replay", "--seed", "4", "--wallets", "30", "--epoch-slots", "120", logPath}, &out); err == nil {
		t.Fatal("replay under another seed succeeded")
	}
	if err := run(ctx, append([]string{"run", "--fake", "--out", filepath.Join(dir, "a")}, flags...), &out); err == nil {
		t.Fatal("run over an existing log succeeded")
	}
}

// A fake run's genesis actors never reach the log as Spawned records, so the
// run's store is the only place their profiles are written.
func TestFakeRunStoresActorProfiles(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "run")
	var out bytes.Buffer
	args := []string{"run", "--fake", "--epochs", "1", "--out", dir, "--seed", "7", "--wallets", "12", "--epoch-slots", "120"}
	if err := run(ctx, args, &out); err != nil {
		t.Fatalf("run: %v\n%s", err, out.String())
	}
	db, err := store.Open(ctx, filepath.Join(dir, "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	rows, err := db.Actors(ctx, store.ActorQuery{SortBy: "id", Limit: store.MaxLimit})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) < 12 {
		t.Fatalf("store holds %d actors, want at least the 12 genesis wallets", len(rows))
	}
	for _, r := range rows {
		if r.Profile == "" {
			t.Fatalf("actor %d has no profile in the store", r.ID)
		}
	}
}

// TestFakeRunM3: the shipped honest population runs on the fake chain with
// its operators on genesis pools: stake delegates and value is conserved.
func TestFakeRunM3(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "run")
	var out bytes.Buffer
	args := []string{"run", "--fake", "--epochs", "2", "--out", dir, "--profiles", "m3", "--pools", "3", "--wallets", "12", "--epoch-slots", "120", "--block-every", "4"}
	if err := run(ctx, args, &out); err != nil {
		t.Fatalf("run: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "value conserved: true") {
		t.Fatalf("run summary: %s", out.String())
	}
	l, err := eventlog.Open(filepath.Join(dir, "events.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	kinds := map[string]int{}
	if err := l.Scan(ctx, 1, func(r eventlog.Record) error {
		if r.Kind == engine.KindSubmitted {
			var s engine.Submitted
			if err := eventlog.Decode(r.Body, &s); err != nil {
				return err
			}
			kinds[s.Kind]++
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if kinds["delegate"] == 0 || kinds["register_stake"] == 0 {
		t.Fatalf("submitted by kind: %v", kinds)
	}
}
