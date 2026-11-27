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

func TestOverrideNeedsAllowFlag(t *testing.T) {
	gap := "-override=maxTxExUnits.memory=140000000"
	if _, err := parse([]string{"-out", "x", gap}); err == nil || !strings.Contains(err.Error(), "maxBlockExUnits") {
		t.Fatalf("parse without -allow-inconsistent-exunits = %v, want a refusal", err)
	}
	o, err := parse([]string{"-out", "x", gap, "-override", "maxBlockExUnits.memory=62000000", "-allow-inconsistent-exunits"})
	if err != nil {
		t.Fatal(err)
	}
	if o.spec.TxExUnits.Memory != 140_000_000 || o.spec.BlockExUnits.Memory != 62_000_000 || !o.spec.Allow.InconsistentExUnits {
		t.Fatalf("spec = %+v", o.spec)
	}
	if _, err := parse([]string{"-out", "x", "-override", "costModels.PlutusV3.length=251"}); err == nil {
		t.Fatal("a short cost model parsed without -allow-short-cost-models")
	}
	if _, err := parse([]string{"-out", "x", "-override", "costModels.PlutusV3.length=251", "-allow-short-cost-models"}); err != nil {
		t.Fatal(err)
	}
	if _, err := parse([]string{"-out", "x", "-override", "systemStart=0"}); err == nil {
		t.Fatal("an unknown override path parsed")
	}
}

func TestParsePublish(t *testing.T) {
	t.Parallel()
	o, err := parse([]string{"-out", "/g"})
	if err != nil {
		t.Fatal(err)
	}
	if o.publish != "" || o.names.ConfigMap != "devnet-genesis" || o.names.Keys != "keys-%d" {
		t.Fatalf("defaults = %q %+v, want no publish to devnet-genesis and keys-%%d", o.publish, o.names)
	}

	o, err = parse([]string{"-out", "/g", "-publish-namespace", "lab", "-genesis-configmap", "g", "-keys-secret", "k%d"})
	if err != nil {
		t.Fatal(err)
	}
	if o.publish != "lab" || o.names.ConfigMap != "g" || o.names.Keys != "k%d" {
		t.Fatalf("parsed %q %+v", o.publish, o.names)
	}

	if _, err := parse([]string{"-out", "/g", "-publish-namespace", "lab", "-keys-secret", "keys"}); err == nil {
		t.Fatal("a keys pattern without a pool index parsed: every pool would share one Secret")
	}
}
