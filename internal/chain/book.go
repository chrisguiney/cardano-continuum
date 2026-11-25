package chain

import (
	"fmt"

	"github.com/blinklabs-io/gouroboros/ledger/common"

	"github.com/chrisguiney/cardano-continuum/internal/population"
)

// Book says which addresses, stake credentials, pools and DReps belong to
// which actor. The view tracks only what the book names, which bounds its
// memory by the engine's own population rather than the chain's.
//
// Register an actor's keys before any transaction can pay or certify them:
// the view does not look back at blocks it has already applied.
type Book struct {
	pay   map[string]population.ActorID
	stake map[common.Blake2b224]population.ActorID
	pools map[common.Blake2b224]population.ActorID
	dreps map[common.Blake2b224]population.ActorID
	// reverse lookups for the agenda's per-actor questions
	stakeOf map[population.ActorID]common.Blake2b224
	poolOf  map[population.ActorID]common.Blake2b224
	drepOf  map[population.ActorID]common.Blake2b224
}

// NewBook returns an empty book.
func NewBook() *Book {
	return &Book{
		pay:     map[string]population.ActorID{},
		stake:   map[common.Blake2b224]population.ActorID{},
		pools:   map[common.Blake2b224]population.ActorID{},
		dreps:   map[common.Blake2b224]population.ActorID{},
		stakeOf: map[population.ActorID]common.Blake2b224{},
		poolOf:  map[population.ActorID]common.Blake2b224{},
		drepOf:  map[population.ActorID]common.Blake2b224{},
	}
}

// AddAddress records that outputs paying a belong to actor id. Script
// addresses the engine deployed to are added the same way.
func (b *Book) AddAddress(a common.Address, id population.ActorID) error {
	k, err := addrKey(a)
	if err != nil {
		return fmt.Errorf("book: add address of actor %d: %w", id, err)
	}
	b.pay[k] = id
	return nil
}

// AddStake records actor id's stake credential hash.
func (b *Book) AddStake(cred common.Blake2b224, id population.ActorID) {
	b.stake[cred] = id
	b.stakeOf[id] = cred
}

// AddPool records the pool operated by actor id, by its cold key hash.
func (b *Book) AddPool(pool common.Blake2b224, id population.ActorID) {
	b.pools[pool] = id
	b.poolOf[id] = pool
}

// AddDRep records actor id's DRep credential hash.
func (b *Book) AddDRep(cred common.Blake2b224, id population.ActorID) {
	b.dreps[cred] = id
	b.drepOf[id] = cred
}

func (b *Book) owner(a common.Address) (population.ActorID, bool) {
	k, err := addrKey(a)
	if err != nil {
		return 0, false
	}
	id, ok := b.pay[k]
	return id, ok
}

func addrKey(a common.Address) (string, error) {
	raw, err := a.Bytes()
	if err != nil {
		return "", err
	}
	return string(raw), nil
}
