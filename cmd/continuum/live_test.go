package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/chrisguiney/cardano-continuum/internal/build"
	"github.com/chrisguiney/cardano-continuum/internal/chain"
	"github.com/chrisguiney/cardano-continuum/internal/rng"
)

func TestRunNeedsFakeOrNode(t *testing.T) {
	var out bytes.Buffer
	err := run(context.Background(), []string{"run", "--out", t.TempDir()}, &out)
	if err == nil || !strings.Contains(err.Error(), "--node") {
		t.Fatalf("run without --fake or --node: %v", err)
	}
	err = run(context.Background(), []string{"run", "--node", "pool-0:3002"}, &out)
	if err == nil || !strings.Contains(err.Error(), "--state") {
		t.Fatalf("live run without --state: %v", err)
	}
}

// TestLiveConfig: a live run takes its clock, k and slot shape from the
// node's genesis, its builder parameters from the node, and its keys from
// the run seed internal/genesis used, with the faucet funding the wallets.
func TestLiveConfig(t *testing.T) {
	g := chain.Genesis{Magic: 42, Start: time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC),
		EpochLength: 500, SecurityParam: 5, SlotLength: time.Second}
	var seed rng.Seed
	seed[0] = 7
	sf := simFlags{wallets: 20, ttl: 120}
	lf := liveFlags{faucet: 1_000_000, refillEvery: 20}
	bp := build.Params{MinFeeA: 44, MinFeeB: 155381, CoinsPerUtxoByte: 4310, MaxTxSize: 16384}
	cfg, err := liveConfig(sf, lf, g, bp, seed)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Seed != seed || cfg.K != 5 || cfg.Slots.EpochStart(1) != 500_000 || cfg.Slots.SlotStart(1) != 1000 {
		t.Fatalf("config %+v", cfg)
	}
	if cfg.Faucet != 1_000_000 || cfg.RefillEvery != 20 || cfg.Build.MinFeeB != 155381 {
		t.Fatalf("faucet %d every %d, build %+v", cfg.Faucet, cfg.RefillEvery, cfg.Build)
	}
	if cfg.Build.SystemStart != g.Start || cfg.Build.SlotLength != g.SlotLength {
		t.Fatalf("build slot timing %v %v", cfg.Build.SystemStart, cfg.Build.SlotLength)
	}
	n := 0
	for _, c := range cfg.Cohorts {
		n += c.Count
	}
	if n != 20 {
		t.Fatalf("%d wallets", n)
	}
	if _, err := cfg.Header(); err != nil {
		t.Fatal(err)
	}
	g.SlotLength = 1500 * time.Microsecond
	if _, err := liveConfig(sf, lf, g, bp, seed); err == nil {
		t.Fatal("a slot length that is not whole milliseconds was accepted")
	}
}
