package main

import (
	"bytes"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadSeedRejectsBadInput(t *testing.T) {
	for _, in := range []string{"", "zz", strings.Repeat("00", 31)} {
		if _, err := readSeed(in); err == nil {
			t.Errorf("readSeed(%q) accepted", in)
		}
	}
	if _, err := readSeed(strings.Repeat("00", 32)); err != nil {
		t.Fatal(err)
	}
}

func TestRunIdempotent(t *testing.T) {
	t.Setenv(seedEnv, strings.Repeat("00", 31)+"01")
	out := t.TempDir()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := run([]string{"-out", out}, log); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(out, "config", "shelley-genesis.json")
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"-out", out, "-start-delay", "1h"}, log); err != nil {
		t.Fatal(err)
	}
	again, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, again) {
		t.Fatal("second run changed the genesis")
	}
	if err := run([]string{"-out", out, "-pools", "2"}, log); err == nil {
		t.Fatal("run over a different spec succeeded")
	}
}
