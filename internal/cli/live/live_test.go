package live

import (
	"testing"
	"time"

	"github.com/chrisguiney/cardano-continuum/internal/build"
	"github.com/chrisguiney/cardano-continuum/internal/chain"
	"github.com/chrisguiney/cardano-continuum/internal/cli/internal/simflags"
	"github.com/chrisguiney/cardano-continuum/internal/rng"
)

// TestLiveConfig: a live run takes its clock, k and slot shape from the
// node's genesis, its builder parameters from the node, and its keys from
// the run seed internal/genesis used, with the faucet funding the wallets.
func TestLiveConfig(t *testing.T) {
	t.Parallel()
	g := chain.Genesis{Magic: 42, Start: time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC),
		EpochLength: 500, SecurityParam: 5, SlotLength: time.Second}
	var seed rng.Seed
	seed[0] = 7
	sf := simflags.Flags{Population: simflags.Population{Wallets: 20, Scale: 1}, Chain: simflags.Chain{TTL: 120}}
	lf := Flags{Faucet: 1_000_000, RefillEvery: 20}
	bp := nodeParams{build: build.Params{MinFeeA: 44, MinFeeB: 155381, CoinsPerUtxoByte: 4310, MaxTxSize: 16384}, nOpt: 3, govLifetime: 6}
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
	if cfg.NOpt != 3 || cfg.GovLifetime != 6 {
		t.Fatalf("view parameters nOpt %d, lifetime %d", cfg.NOpt, cfg.GovLifetime)
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
