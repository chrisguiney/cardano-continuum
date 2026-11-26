package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chrisguiney/cardano-continuum/internal/forger"
	"github.com/chrisguiney/cardano-continuum/internal/genesis"
	"github.com/chrisguiney/cardano-continuum/internal/keys"
	"github.com/chrisguiney/cardano-continuum/internal/rng"
)

func devnet(t *testing.T) string {
	t.Helper()
	store, err := keys.New(rng.Seed{3}, 0)
	if err != nil {
		t.Fatal(err)
	}
	s := genesis.Default()
	s.SystemStart = time.Now().Add(time.Hour).UTC()
	b, err := genesis.Render(s, store)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if _, err := genesis.WriteDir(dir, b); err != nil {
		t.Fatal(err)
	}
	return dir
}

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// TestGate: the keys gate passes on a pool's keys and fails on a blank
// Secret, which is what keeps a pod pending while the engine reissues.
func TestGate(t *testing.T) {
	dir := devnet(t)
	keysDir := filepath.Join(dir, "pools", "0")
	if err := run(context.Background(), []string{"gate", "--keys", keysDir}, quiet); err != nil {
		t.Fatalf("gate on issued keys: %v", err)
	}
	if err := os.WriteFile(filepath.Join(keysDir, forger.CertFile), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run(context.Background(), []string{"gate", "--keys", keysDir}, quiet); err == nil {
		t.Fatal("gate passed with a blank operational certificate")
	}
}

// TestRunRefusesBadSchedule: a schedule naming a misbehaviour this build
// does not know stops the forger before it listens.
func TestRunRefusesBadSchedule(t *testing.T) {
	dir := devnet(t)
	keysDir := filepath.Join(dir, "pools", "0")
	doc := `{"seed":"` + strings.Repeat("00", 32) + `","rules":[{"kind":"no-such","rate":1}]}`
	if err := os.WriteFile(filepath.Join(keysDir, forger.BehaviourFile), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	err := run(context.Background(), []string{"run", "--genesis", filepath.Join(dir, "config"), "--keys", keysDir,
		"--listen", "127.0.0.1:0"}, quiet)
	if err == nil || !strings.Contains(err.Error(), "no-such") {
		t.Fatalf("run = %v, want the unknown misbehaviour refused", err)
	}
}

// TestRunStopsOnCancel: the forger starts on a rendered devnet and returns
// when its context ends, here by a deadline.
func TestRunStopsOnCancel(t *testing.T) {
	dir := devnet(t)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, []string{"run", "--genesis", filepath.Join(dir, "config"),
			"--keys", filepath.Join(dir, "pools", "0"), "--listen", "127.0.0.1:0",
			"--evidence", filepath.Join(t.TempDir(), "evidence")}, quiet)
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run did not return after cancel")
	}
}
