package engine

import (
	"bytes"
	"fmt"
	"slices"

	"github.com/blinklabs-io/gouroboros/ledger/common"

	"github.com/chrisguiney/cardano-continuum/internal/chain"
	"github.com/chrisguiney/cardano-continuum/internal/fakechain"
	"github.com/chrisguiney/cardano-continuum/internal/keys"
	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
)

// Genesis pools are the ones internal/genesis registers: pool i is the root
// keystore's pool i, and its owner (the root keystore's wallet i) holds
// GenesisPoolStake delegated to it. The first GenesisPools pool actors of
// the cohorts operate them. The owners are not actors: the engine never
// spends their funds, but their stake is in every schedule the stake-floor
// guard reads, so the view starts with it, as the ledger does.

// genesisPool is one genesis pool and the actor operating it.
type genesisPool struct {
	Index int
	Actor population.ActorID
	Owner keys.Wallet
	ID    [28]byte
	VRF   [32]byte
}

// ownerActor names genesis pool i's owner in the view's book: below the
// script actor, above every census ID.
func ownerActor(i int) population.ActorID { return scriptActor - 1 - population.ActorID(i) }

// genesisPools lists the genesis pools and their operators. root is the
// keystore of the run seed itself.
func (c *Config) genesisPools(root *keys.Store) ([]genesisPool, error) {
	if c.GenesisPools == 0 {
		return nil, nil
	}
	var out []genesisPool
	id := population.ActorID(0)
	for _, co := range c.Cohorts {
		p, _ := c.Profiles.Get(co.Profile)
		for range co.Count {
			id++
			if p.Kind != profile.Pool || len(out) == c.GenesisPools {
				continue
			}
			i := len(out)
			pk, err := root.Pool(keys.ActorID(i), 0)
			if err != nil {
				return nil, fmt.Errorf("engine: genesis pool %d: %w", i, err)
			}
			out = append(out, genesisPool{Index: i, Actor: id, Owner: root.Wallet(keys.ActorID(i)), ID: pk.PoolID, VRF: pk.VRFVKeyHash})
		}
	}
	if len(out) < c.GenesisPools {
		return nil, fmt.Errorf("%w: %d genesis pools, %d pool actors", ErrConfig, c.GenesisPools, len(out))
	}
	return out, nil
}

// genesisRef is a Shelley genesis output's reference: (Blake2b-256 of the
// address, 0).
func genesisRef(a common.Address) (Ref, error) {
	raw, err := a.Bytes()
	if err != nil {
		return Ref{}, err
	}
	return Ref{Tx: TxID(blake2b256(raw))}, nil
}

// stakeGenesis adds the genesis pools to the view's genesis state: the
// pools, their owners' outputs, registrations and delegations, and the
// mark and set snapshots the first epochs' schedules come from.
func (e *Engine) stakeGenesis(s *chain.State) error {
	var entries chain.Snapshot
	for _, g := range e.gpools {
		ref, err := genesisRef(g.Owner.Address())
		if err != nil {
			return err
		}
		cred := common.Blake2b224(g.Owner.StakeKeyHash())
		s.Utxos = append(s.Utxos, ownedUtxo(g.Owner.Address(), ref, e.cfg.GenesisPoolStake))
		s.Stake = append(s.Stake, chain.StakeState{Cred: cred, Registered: true, Delegated: true, Pool: common.Blake2b224(g.ID)})
		s.Pools = append(s.Pools, chain.PoolState{ID: common.Blake2b224(g.ID)})
		entries = append(entries, chain.StakeEntry{Cred: cred, Pool: common.Blake2b224(g.ID), Stake: e.cfg.GenesisPoolStake})
	}
	slices.SortFunc(entries, func(a, b chain.StakeEntry) int { return bytes.Compare(a.Cred[:], b.Cred[:]) })
	s.Mark, s.Set = entries, slices.Clone(entries)
	return nil
}

// bookGenesis names the genesis owners in the view's book.
func (e *Engine) bookGenesis() error {
	for _, g := range e.gpools {
		if err := e.book.AddAddress(g.Owner.Address(), ownerActor(g.Index)); err != nil {
			return fmt.Errorf("engine: book: %w", err)
		}
		e.book.AddStake(common.Blake2b224(g.Owner.StakeKeyHash()), ownerActor(g.Index))
	}
	return nil
}

// genesisOf is the genesis pool an actor operates, if it operates one.
func (e *Engine) genesisOf(id population.ActorID) (genesisPool, bool) {
	for _, g := range e.gpools {
		if g.Actor == id {
			return g, true
		}
	}
	return genesisPool{}, false
}

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
