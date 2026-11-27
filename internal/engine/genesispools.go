package engine

import (
	"bytes"
	"errors"
	"fmt"
	"maps"
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

// poolGroup is a run of count actors of one profile kind, in cohort order.
type poolGroup struct {
	kind  profile.Kind
	count int
}

// shortPools: fewer pool actors than genesis pools.
type shortPools struct{ want, have int }

func (e shortPools) Error() string {
	return fmt.Sprintf("%d genesis pools, %d pool actors", e.want, e.have)
}

// genesisPoolsOf lists the first want pool actors of groups as the operators
// of the genesis pools. root is the keystore of the run seed itself.
func genesisPoolsOf(want int, groups []poolGroup, root *keys.Store) ([]genesisPool, error) {
	if want == 0 {
		return nil, nil
	}
	var out []genesisPool
	id := population.ActorID(0)
	for _, g := range groups {
		for range g.count {
			id++
			if g.kind != profile.Pool || len(out) == want {
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
	if len(out) < want {
		return nil, shortPools{want: want, have: len(out)}
	}
	return out, nil
}

// genesisPools lists the genesis pools and their operators. root is the
// keystore of the run seed itself.
func (c *Config) genesisPools(root *keys.Store) ([]genesisPool, error) {
	groups := make([]poolGroup, len(c.Cohorts))
	for i, co := range c.Cohorts {
		p, _ := c.Profiles.Get(co.Profile)
		groups[i] = poolGroup{kind: p.Kind, count: co.Count}
	}
	out, err := genesisPoolsOf(c.GenesisPools, groups, root)
	if errors.As(err, &shortPools{}) {
		return nil, fmt.Errorf("%w: %w", ErrConfig, err)
	}
	return out, err
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

// gset is the genesis pools and what they put in the chain view's
// genesis state and book.
type gset struct {
	pools []genesisPool
	// stake is what each pool's owner holds, delegated to the pool.
	stake uint64
	view  chainView
}

// of is the genesis pool an actor operates, if it operates one.
func (g *gset) of(id population.ActorID) (genesisPool, bool) {
	for _, p := range g.pools {
		if p.Actor == id {
			return p, true
		}
	}
	return genesisPool{}, false
}

// seed adds the genesis pools to the view's genesis state: the pools, their
// owners' outputs, registrations and delegations, and the mark and set
// snapshots the first epochs' schedules come from. utxo builds an output.
func (g *gset) seed(s *chain.State, utxo func(common.Address, Ref, uint64) common.Utxo) error {
	var entries chain.Snapshot
	for _, p := range g.pools {
		ref, err := genesisRef(p.Owner.Address())
		if err != nil {
			return err
		}
		cred := common.Blake2b224(p.Owner.StakeKeyHash())
		s.Utxos = append(s.Utxos, utxo(p.Owner.Address(), ref, g.stake))
		s.Stake = append(s.Stake, chain.StakeState{Cred: cred, Registered: true, Delegated: true, Pool: common.Blake2b224(p.ID)})
		s.Pools = append(s.Pools, chain.PoolState{ID: common.Blake2b224(p.ID)})
		entries = append(entries, chain.StakeEntry{Cred: cred, Pool: common.Blake2b224(p.ID), Stake: g.stake})
	}
	slices.SortFunc(entries, func(a, b chain.StakeEntry) int { return bytes.Compare(a.Cred[:], b.Cred[:]) })
	s.Mark, s.Set = entries, slices.Clone(entries)
	return nil
}

// book names the genesis owners in the view's book.
func (g *gset) book() error {
	for _, p := range g.pools {
		if err := g.view.Book().AddAddress(p.Owner.Address(), ownerActor(p.Index)); err != nil {
			return fmt.Errorf("engine: book: %w", err)
		}
		g.view.Book().AddStake(common.Blake2b224(p.Owner.StakeKeyHash()), ownerActor(p.Index))
	}
	return nil
}

// keyring is the keys of the population: the actors' wallets and DRep keys
// from the actors' keystore, the genesis pools' keys from the root one, and
// which pool each pool actor operates.
type keyring struct {
	actors, root *keys.Store
	gen          *gset
	// pools maps each pool actor to the pool it operates.
	pools map[population.ActorID][28]byte
}

func newKeyring(actors, root *keys.Store, gen *gset) *keyring {
	return &keyring{actors: actors, root: root, gen: gen, pools: map[population.ActorID][28]byte{}}
}

// bind adds an actor's keys to the view's book: its address and stake
// credential, and its pool or DRep key by profile kind.
func (k *keyring) bind(book *chain.Book, id population.ActorID, p profile.Profile, w keys.Wallet) {
	_ = book.AddAddress(w.Address(), id)
	book.AddStake(common.Blake2b224(w.StakeKeyHash()), id)
	switch p.Kind {
	case profile.Pool:
		if g, ok := k.gen.of(id); ok {
			book.AddPool(common.Blake2b224(g.ID), id)
			k.pools[id] = g.ID
		} else if pool, ok := k.poolID(id); ok {
			book.AddPool(pool, id)
			k.pools[id] = pool
		}
	case profile.DRep:
		book.AddDRep(common.Blake2b224(k.drep(id)), id)
	}
}

// poolID is the pool a pool actor operates: its cold key hash.
func (k *keyring) poolID(id population.ActorID) (common.Blake2b224, bool) {
	s, err := k.actors.PoolSigner(keys.ActorID(id))
	if err != nil {
		return common.Blake2b224{}, false
	}
	return common.Blake2b224(s.KeyHash()), true
}

// poolOf is the pool a pool actor operates.
func (k *keyring) poolOf(id population.ActorID) ([28]byte, bool) {
	p, ok := k.pools[id]
	return p, ok
}

// poolIDs lists the pool actors, ascending.
func (k *keyring) poolIDs() []population.ActorID { return slices.Sorted(maps.Keys(k.pools)) }

// poolKeys is the keystore and index a pool actor's pool keys come from:
// the root keystore for a genesis pool, the actors' own otherwise.
func (k *keyring) poolKeys(id population.ActorID) (*keys.Store, keys.ActorID) {
	if g, ok := k.gen.of(id); ok {
		return k.root, keys.ActorID(g.Index)
	}
	return k.actors, keys.ActorID(id)
}

func (k *keyring) poolSigner(id population.ActorID) (keys.Signer, error) {
	ks, i := k.poolKeys(id)
	return ks.PoolSigner(i)
}

// drep is the DRep credential of a DRep actor.
func (k *keyring) drep(id population.ActorID) keys.Credential { return k.actors.DRep(keys.ActorID(id)) }

func (k *keyring) drepSigner(id population.ActorID) keys.Signer {
	return k.actors.DRepSigner(keys.ActorID(id))
}

// committee is committee member i's cold and hot signers, from the root
// keystore as internal/genesis seats them.
func (k *keyring) committee(i uint32) (cold, hot keys.Signer) { return k.root.CommitteeSigners(i) }

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
