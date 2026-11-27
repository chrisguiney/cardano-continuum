package policy

import (
	"cmp"
	"slices"

	"github.com/chrisguiney/cardano-continuum/internal/population"
)

// Plan is the recovery the engine submits within the current epoch.
type Plan struct {
	// GoLive lists every registered dark pool with no retirement confirmed
	// or in flight.
	GoLive []population.ActorID
	// Repair lists every pool planned live (or in GoLive) whose pod is not
	// running: reschedule it, or issue a new opcert. It needs no
	// transaction, so it is the fastest way back.
	Repair []population.ActorID
	// Moves redelegates every wallet on a pool that does not forge to one
	// that does.
	Moves []Intent
	// Waiting lists wallets on a pool that does not forge whose own
	// transaction in flight goes elsewhere; the engine cannot replace it
	// and moves them once it lands or expires.
	Waiting []population.ActorID
}

func sortedPools(ps []Pool) []Pool {
	out := slices.Clone(ps)
	slices.SortFunc(out, func(a, b Pool) int { return cmp.Compare(a.ID, b.ID) })
	return out
}

// moves redelegates the wallets on pools outside f to pools in f. A target
// is counted as forging once GoLive and Repair are done, which a dead pod
// is not: its wallets move even while it is being repaired, since a repair
// may not succeed.
func moves(s State, f map[population.ActorID]bool) ([]Intent, []population.ActorID) {
	if len(f) == 0 {
		return nil, nil
	}
	proj, err := project(s.Deleg, s.InFlight)
	if err != nil {
		proj = s.Deleg
	}
	load := make(map[population.ActorID]uint64, len(f))
	for id := range f {
		load[id] = 0
	}
	for _, e := range proj {
		if f[e.Pool] {
			load[e.Pool] += e.Stake
		}
	}
	pending := map[population.ActorID]Intent{}
	for _, tx := range s.InFlight {
		if tx.Op == Delegate || tx.Op == Undelegate {
			pending[tx.Wallet] = tx
		}
	}
	wallets := slices.Clone(s.Deleg)
	slices.SortFunc(wallets, func(a, b Delegation) int { return cmp.Compare(a.Wallet, b.Wallet) })
	var mv []Intent
	var wait []population.ActorID
	for _, e := range wallets {
		if e.Pool == 0 || f[e.Pool] {
			continue
		}
		if tx, ok := pending[e.Wallet]; ok {
			if tx.Op != Delegate || !f[tx.Pool] {
				wait = append(wait, e.Wallet)
			}
			continue
		}
		to := lightest(load)
		load[to] += e.Stake
		mv = append(mv, Intent{Op: Delegate, Wallet: e.Wallet, Pool: to})
	}
	return mv, wait
}

// lightest is the pool with the least load, the lower ID on a tie. Every
// pool in f has an entry, if only zero, so load is never empty here.
func lightest(load map[population.ActorID]uint64) population.ActorID {
	var best population.ActorID
	found := false
	for id, l := range load {
		if !found || l < load[best] || (l == load[best] && id < best) {
			best, found = id, true
		}
	}
	return best
}
