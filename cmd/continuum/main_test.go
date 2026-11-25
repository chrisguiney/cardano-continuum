package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDeterminismCLIRuns is `make determinism`'s double run: two fake runs
// with one seed write byte-identical logs, replay reproduces them, and a
// replay under another seed is refused.
func TestDeterminismCLIRuns(t *testing.T) {
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
