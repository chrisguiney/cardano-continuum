package chain

import (
	"cmp"
	"slices"

	"github.com/blinklabs-io/gouroboros/ledger/common"

	"github.com/chrisguiney/cardano-continuum/internal/agenda"
	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
)

// The methods below answer the agenda's per-actor questions (agenda.View)
// from the view at its tip.

// Balance is the lovelace in the actor's tracked outputs.
func (v *View) Balance(id population.ActorID) profile.Lovelace {
	return profile.Lovelace(v.balance[id])
}

// Epoch is the epoch of the tip's slot.
func (v *View) Epoch() uint64 { return v.cfg.Slots.EpochOfSlot(v.tip.Slot) }

// StakeRegistered reports whether the actor's stake credential is registered.
func (v *View) StakeRegistered(id population.ActorID) bool {
	cred, ok := v.cfg.Book.stakeOf[id]
	return ok && v.stake[cred].Registered
}

// DelegatedTo is the pool actor the actor's stake is delegated to. A
// delegation to a pool the book does not name reports false.
func (v *View) DelegatedTo(id population.ActorID) (population.ActorID, bool) {
	cred, ok := v.cfg.Book.stakeOf[id]
	if !ok {
		return 0, false
	}
	s := v.stake[cred]
	if !s.Delegated {
		return 0, false
	}
	pool, ok := v.cfg.Book.pools[s.Pool]
	return pool, ok
}

// PoolRegistered reports whether the pool actor's pool is registered and
// not yet past its retirement epoch.
func (v *View) PoolRegistered(id population.ActorID) bool {
	pool, ok := v.cfg.Book.poolOf[id]
	return ok && v.poolLive(pool)
}

// DRepRegistered reports whether the actor's DRep credential is registered.
func (v *View) DRepRegistered(id population.ActorID) bool {
	cred, ok := v.cfg.Book.drepOf[id]
	return ok && v.dreps[cred]
}

// Pools lists the registered pools the book names, by actor ID, with the
// returns last given to SetReturns.
func (v *View) Pools() []agenda.PoolInfo {
	var out []agenda.PoolInfo
	for id, st := range v.pools {
		actor := v.cfg.Book.pools[id]
		if v.poolLive(st.ID) {
			out = append(out, agenda.PoolInfo{ID: actor, ROI: v.returns[actor]})
		}
	}
	slices.SortFunc(out, func(a, b agenda.PoolInfo) int { return cmp.Compare(a.ID, b.ID) })
	return out
}

// SetReturns sets each pool actor's expected return per epoch, which comes
// from local-state-query at epoch boundaries rather than from blocks. It is
// not part of the fold: Snapshot, Base and Rollback ignore it.
func (v *View) SetReturns(roi map[population.ActorID]float64) {
	v.returns = roi
}

func (v *View) poolLive(id common.Blake2b224) bool {
	p, ok := v.pools[id]
	return ok && (!p.Retiring || v.Epoch() < p.RetireAt)
}
