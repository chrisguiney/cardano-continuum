package chain

import (
	"maps"

	"github.com/blinklabs-io/gouroboros/ledger/common"

	"github.com/chrisguiney/cardano-continuum/internal/population"
)

// registry is the view's pools: the registrations and retirements of the
// pools the book names, and the returns last given to SetReturns.
type registry struct {
	book    *Book
	pools   map[common.Blake2b224]PoolState
	returns map[population.ActorID]float64
}

func newRegistry(b *Book) registry {
	return registry{book: b, pools: map[common.Blake2b224]PoolState{}}
}

// clone copies the pools. returns is shared: SetReturns replaces the map
// and nothing writes into it.
func (r registry) clone() registry {
	r.pools = maps.Clone(r.pools)
	return r
}

func (r *registry) editPool(id common.Blake2b224, u *undo, edit func(*PoolState)) bool {
	if _, ok := r.book.pools[id]; !ok {
		return false
	}
	remember(&u.pools, r.pools, id)
	p := r.pools[id]
	p.ID = id
	edit(&p)
	r.pools[id] = p
	return true
}

// PoolActor is the pool actor the book names for a pool id.
func (r *registry) PoolActor(pool common.Blake2b224) (population.ActorID, bool) {
	id, ok := r.book.pools[pool]
	return id, ok
}

// SetReturns sets each pool actor's expected return per epoch, which comes
// from local-state-query at epoch boundaries rather than from blocks. It is
// not part of the fold: Snapshot, Base and Rollback ignore it.
func (r *registry) SetReturns(roi map[population.ActorID]float64) {
	r.returns = roi
}

// live reports whether the pool is registered and not past its retirement
// epoch at epoch ep.
func (r *registry) live(id common.Blake2b224, ep uint64) bool {
	p, ok := r.pools[id]
	return ok && !p.Retired && (!p.Retiring || ep < p.RetireAt)
}

// reap retires the pools whose retirement epoch is at most ep and drops
// the delegations to them.
func (r *registry) reap(ep uint64, a *accounts, u *undo) {
	for _, id := range sortedKeys(r.pools) {
		p := r.pools[id]
		if !p.Retiring || p.RetireAt > ep || p.Retired {
			continue
		}
		remember(&u.pools, r.pools, id)
		p.Retired = true
		r.pools[id] = p
		a.undelegate(id, u)
	}
}
