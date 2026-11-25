package policy

import (
	"cmp"
	"slices"

	"github.com/chrisguiney/cardano-continuum/internal/population"
)

// Endangered reports that the floor is breached, or will be, in a schedule
// already on its way: any of the guard's three schedules, counting only the
// pools that really forge (registered, planned live, pod running) and with
// everything in flight landed. No margin is applied. Recovery must start on
// this, not on a breach of the schedule in use alone: a pod that dies after
// the epoch's last block can leave this epoch above the floor and the next
// one below it, and waiting loses the epoch in which a move could land.
func (g Guard) Endangered(s State) bool {
	proj, err := project(s.Deleg, s.InFlight)
	if err != nil {
		return true
	}
	ws, err := s.horizon(proj)
	if err != nil {
		return true
	}
	f := map[population.ActorID]bool{}
	for _, p := range s.Pools {
		if p.Reg == Registered && p.Live && p.Running {
			f[p.ID] = true
		}
	}
	strict := Guard{cfg: Config{Floor: g.cfg.Floor}}
	for _, w := range ws {
		if !strict.meets(w, f).OK {
			return true
		}
	}
	return false
}

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

// Recover returns the recovery for s, or an empty Plan when s is not
// Endangered or the ledger is Frozen (contract rules 12 to 14). Every list
// is in ascending ID order, and move targets are chosen least projected
// stake first, ties to the lower ID, so the plan depends only on s.
func (g Guard) Recover(s State) Plan {
	if s.Frozen() || !g.Endangered(s) {
		return Plan{}
	}
	var p Plan
	ret := s.retiring(nil)
	f := map[population.ActorID]bool{}
	for _, pl := range sortedPools(s.Pools) {
		if pl.Reg != Registered || ret[pl.ID] {
			continue // rule 9: a pool with a retirement in flight stays dark
		}
		if !pl.Live {
			p.GoLive = append(p.GoLive, pl.ID)
		}
		if !pl.Running {
			p.Repair = append(p.Repair, pl.ID)
			continue
		}
		f[pl.ID] = true
	}
	p.Moves, p.Waiting = moves(s, f)
	return p
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
