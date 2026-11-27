package cohort

import (
	"maps"
	"slices"

	"github.com/blinklabs-io/gouroboros/ledger/common"

	"github.com/chrisguiney/cardano-continuum/internal/chain"
	"github.com/chrisguiney/cardano-continuum/internal/keys"
	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
)

// Keyring is the keys of the population: the actors' wallets and DRep keys
// from the actors' keystore, the genesis pools' keys from the root one, and
// which pool each pool actor operates.
type Keyring struct {
	actors, root *keys.Store
	// Gen is the genesis pools.
	Gen *Genesis
	// pools maps each pool actor to the pool it operates.
	pools map[population.ActorID][28]byte
}

func newKeyring(actors, root *keys.Store, gen *Genesis) *Keyring {
	return &Keyring{actors: actors, root: root, Gen: gen, pools: map[population.ActorID][28]byte{}}
}

// bind adds an actor's keys to the view's book: its address and stake
// credential, and its pool or DRep key by profile kind.
func (k *Keyring) bind(book *chain.Book, id population.ActorID, p profile.Profile, w keys.Wallet) {
	_ = book.AddAddress(w.Address(), id)
	book.AddStake(common.Blake2b224(w.StakeKeyHash()), id)
	switch p.Kind {
	case profile.Pool:
		if g, ok := k.Gen.Of(id); ok {
			book.AddPool(common.Blake2b224(g.ID), id)
			k.pools[id] = g.ID
		} else if pool, ok := k.poolID(id); ok {
			book.AddPool(pool, id)
			k.pools[id] = pool
		}
	case profile.DRep:
		book.AddDRep(common.Blake2b224(k.DRep(id)), id)
	}
}

// poolID is the pool a pool actor operates: its cold key hash.
func (k *Keyring) poolID(id population.ActorID) (common.Blake2b224, bool) {
	s, err := k.actors.PoolSigner(keys.ActorID(id))
	if err != nil {
		return common.Blake2b224{}, false
	}
	return common.Blake2b224(s.KeyHash()), true
}

// PoolOf is the pool a pool actor operates.
func (k *Keyring) PoolOf(id population.ActorID) ([28]byte, bool) {
	p, ok := k.pools[id]
	return p, ok
}

// PoolIDs lists the pool actors, ascending.
func (k *Keyring) PoolIDs() []population.ActorID { return slices.Sorted(maps.Keys(k.pools)) }

// PoolKeys is the keystore and index a pool actor's pool keys come from:
// the root keystore for a genesis pool, the actors' own otherwise.
func (k *Keyring) PoolKeys(id population.ActorID) (*keys.Store, keys.ActorID) {
	if g, ok := k.Gen.Of(id); ok {
		return k.root, keys.ActorID(g.Index)
	}
	return k.actors, keys.ActorID(id)
}

// PoolSigner is the pool actor's cold key signer.
func (k *Keyring) PoolSigner(id population.ActorID) (keys.Signer, error) {
	ks, i := k.PoolKeys(id)
	return ks.PoolSigner(i)
}

// DRep is the DRep credential of a DRep actor.
func (k *Keyring) DRep(id population.ActorID) keys.Credential { return k.actors.DRep(keys.ActorID(id)) }

// DRepSigner is the DRep actor's signer.
func (k *Keyring) DRepSigner(id population.ActorID) keys.Signer {
	return k.actors.DRepSigner(keys.ActorID(id))
}

// SeatCommittee names committee seats 0..n-1 in the view's book by their
// hot keys, so the view tracks their votes.
func (k *Keyring) SeatCommittee(book *chain.Book, n int) {
	for i := range n {
		_, hot := k.Committee(uint32(i))
		book.AddCommittee(common.Blake2b224(hot.KeyHash()), i)
	}
}

// Committee is committee member i's cold and hot signers, from the root
// keystore as internal/genesis seats them.
func (k *Keyring) Committee(i uint32) (cold, hot keys.Signer) { return k.root.CommitteeSigners(i) }
