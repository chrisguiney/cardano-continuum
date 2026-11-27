package cohort

import (
	"bytes"
	"fmt"
	"slices"

	"github.com/blinklabs-io/gouroboros/ledger/common"

	"github.com/chrisguiney/cardano-continuum/internal/chain"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/wire"
	"github.com/chrisguiney/cardano-continuum/internal/keys"
	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
)

// Genesis pools are the ones internal/genesis registers: pool i is the root
// keystore's pool i, and its owner (the root keystore's wallet i) holds
// the pool stake delegated to it. The first pool actors of the cohorts
// operate them. The owners are not actors: the engine never spends their
// funds, but their stake is in every schedule the stake-floor guard reads,
// so the view starts with it, as the ledger does.

// GenesisPool is one genesis pool and the actor operating it.
type GenesisPool struct {
	Index int
	Actor population.ActorID
	Owner keys.Wallet
	ID    [28]byte
	VRF   [32]byte
}

// ownerActor names genesis pool i's owner in the view's book: below the
// script actor, above every census ID.
func ownerActor(i int) population.ActorID { return Script - 1 - population.ActorID(i) }

// Group is a run of Count actors of one profile kind, in cohort order.
type Group struct {
	Kind  profile.Kind
	Count int
}

// ShortError: fewer pool actors than genesis pools.
type ShortError struct{ Want, Have int }

func (e ShortError) Error() string {
	return fmt.Sprintf("%d genesis pools, %d pool actors", e.Want, e.Have)
}

// GenesisPools lists the first want pool actors of groups as the operators
// of the genesis pools. root is the keystore of the run seed itself.
func GenesisPools(want int, groups []Group, root *keys.Store) ([]GenesisPool, error) {
	if want == 0 {
		return nil, nil
	}
	var out []GenesisPool
	id := population.ActorID(0)
	for _, g := range groups {
		for range g.Count {
			id++
			if g.Kind != profile.Pool || len(out) == want {
				continue
			}
			i := len(out)
			pk, err := root.Pool(keys.ActorID(i), 0)
			if err != nil {
				return nil, fmt.Errorf("engine: genesis pool %d: %w", i, err)
			}
			out = append(out, GenesisPool{Index: i, Actor: id, Owner: root.Wallet(keys.ActorID(i)), ID: pk.PoolID, VRF: pk.VRFVKeyHash})
		}
	}
	if len(out) < want {
		return nil, ShortError{Want: want, Have: len(out)}
	}
	return out, nil
}

// genesisRef is a Shelley genesis output's reference: (Blake2b-256 of the
// address, 0).
func genesisRef(a common.Address) (wire.Ref, error) {
	raw, err := a.Bytes()
	if err != nil {
		return wire.Ref{}, err
	}
	return wire.Ref{Tx: wire.TxID(common.Blake2b256Hash(raw))}, nil
}

// Genesis is the genesis pools and what they put in the chain view's
// genesis state and book.
type Genesis struct {
	Pools []GenesisPool
	// stake is what each pool's owner holds, delegated to the pool.
	stake uint64
	chain Chain
}

// Of is the genesis pool an actor operates, if it operates one.
func (g *Genesis) Of(id population.ActorID) (GenesisPool, bool) {
	for _, p := range g.Pools {
		if p.Actor == id {
			return p, true
		}
	}
	return GenesisPool{}, false
}

// Seed adds the genesis pools to the view's genesis state: the pools, their
// owners' outputs, registrations and delegations, and the mark and set
// snapshots the first epochs' schedules come from. utxo builds an output.
func (g *Genesis) Seed(s *chain.State, utxo func(common.Address, wire.Ref, uint64) common.Utxo) error {
	var entries chain.Snapshot
	for _, p := range g.Pools {
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

// Book names the genesis owners in the view's book.
func (g *Genesis) Book() error {
	for _, p := range g.Pools {
		if err := g.chain.Book().AddAddress(p.Owner.Address(), ownerActor(p.Index)); err != nil {
			return fmt.Errorf("engine: book: %w", err)
		}
		g.chain.Book().AddStake(common.Blake2b224(p.Owner.StakeKeyHash()), ownerActor(p.Index))
	}
	return nil
}
