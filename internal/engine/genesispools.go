package engine

import (
	"fmt"

	"github.com/blinklabs-io/gouroboros/ledger/common"

	"github.com/chrisguiney/cardano-continuum/internal/fakechain"
	"github.com/chrisguiney/cardano-continuum/internal/keys"
)

// GenesisStaking is the fake chain's genesis delegation for cfg: each
// genesis pool registered, its owner funded and delegated to it.
func GenesisStaking(cfg *Config) ([]fakechain.GenesisOutput, []fakechain.GenesisStake, error) {
	root, err := keys.New(cfg.Seed, cfg.NetworkID)
	if err != nil {
		return nil, nil, fmt.Errorf("engine: root keystore: %w", err)
	}
	gs, err := cfg.genesisPools(root)
	if err != nil {
		return nil, nil, err
	}
	var outs []fakechain.GenesisOutput
	var stake []fakechain.GenesisStake
	for _, g := range gs {
		owner := common.AddrKeyHash(g.Owner.StakeKeyHash())
		outs = append(outs, fakechain.GenesisOutput{Address: g.Owner.Address(), Lovelace: cfg.GenesisPoolStake})
		stake = append(stake, fakechain.GenesisStake{
			Pool: common.PoolRegistrationCertificate{
				Operator: common.PoolKeyHash(g.ID), VrfKeyHash: common.VrfKeyHash(g.VRF), Pledge: cfg.GenesisPoolStake,
				Cost: cfg.Build.MinPoolCost, RewardAccount: owner, PoolOwners: []common.AddrKeyHash{owner},
			},
			Delegators: []common.Blake2b224{common.Blake2b224(owner)},
		})
	}
	return outs, stake, nil
}

// GenesisPoolActors maps each genesis pool's index to the pool actor that
// operates it, so a live run can find the pool's ContinuumPool.
func (c *Config) GenesisPoolActors() (map[int]uint64, error) {
	root, err := keys.New(c.Seed, c.NetworkID)
	if err != nil {
		return nil, fmt.Errorf("engine: root keystore: %w", err)
	}
	gs, err := c.genesisPools(root)
	if err != nil {
		return nil, err
	}
	out := map[int]uint64{}
	for _, g := range gs {
		out[g.Index] = uint64(g.Actor)
	}
	return out, nil
}
