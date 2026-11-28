package engine

import (
	"fmt"
	"time"

	"github.com/blinklabs-io/gouroboros/ledger/common"

	"github.com/chrisguiney/cardano-continuum/internal/chain"
	"github.com/chrisguiney/cardano-continuum/internal/fakechain"
	"github.com/chrisguiney/cardano-continuum/internal/keys"
)

// NewFakeChain returns a fake chain holding cfg's genesis funds, with the
// given faults scheduled.
func NewFakeChain(cfg *Config, faults ...fakechain.Scheduled) (*fakechain.Chain, error) {
	funds, err := cfg.Genesis()
	if err != nil {
		return nil, err
	}
	gs := make([]fakechain.GenesisOutput, len(funds))
	for i, f := range funds {
		gs[i] = fakechain.GenesisOutput{Address: f.Address, Lovelace: f.Lovelace}
	}
	owners, staking, err := GenesisStaking(cfg)
	if err != nil {
		return nil, err
	}
	root, err := keys.New(cfg.Seed, cfg.NetworkID)
	if err != nil {
		return nil, fmt.Errorf("engine: root keystore: %w", err)
	}
	var seats []fakechain.CommitteeSeat
	for i := range cfg.Committee {
		cold, _ := root.Committee(uint32(i))
		seats = append(seats, fakechain.CommitteeSeat{Cold: common.Blake2b224(cold), Expiry: 1 << 20})
	}
	return fakechain.New(fakechain.Config{
		Committee: seats,
		Slots:     cfg.Slots, NetworkID: cfg.NetworkID, K: cfg.K, BlockEvery: cfg.BlockEvery, Genesis: append(gs, owners...),
		Faults: faults, Staking: staking, Params: cfg.ChainParams, Treasury: cfg.StartTreasury(),
	})
}

// FakeGenesis is the genesis identity of cfg's fake chain, as a devnet
// with network magic would answer it: what adversary.Clear compares for a
// fake run.
func FakeGenesis(cfg *Config, magic uint32) chain.Genesis {
	return chain.Genesis{
		Magic: magic, Start: time.UnixMilli(0).UTC(), EpochLength: cfg.Slots.SlotAt(cfg.Slots.EpochStart(1)),
		SecurityParam: cfg.K, SlotLength: time.Duration(cfg.Slots.SlotStart(1)) * time.Millisecond,
	}
}
