package chain

import (
	"slices"

	"github.com/chrisguiney/cardano-continuum/internal/agenda"
	"github.com/chrisguiney/cardano-continuum/internal/policy"
)

// policyState is View.PolicyState.
func policyState(v *View, epoch uint64, pods agenda.Pods, inflight []policy.Intent) policy.State {
	c := v.Confirmed()
	st := policy.State{
		Epoch:    epoch,
		Label:    v.ep.label,
		Set:      schedule(v.cfg.Book, v.ep.set),
		Mark:     schedule(v.cfg.Book, v.ep.mark),
		Deleg:    c.delegation(c.credStake()),
		InFlight: slices.Clone(inflight),
	}
	for _, id := range sortedKeys(c.pools) {
		actor, ok := v.cfg.Book.pools[id]
		if !ok {
			continue
		}
		p := c.pools[id]
		reg := policy.Registered
		switch {
		case p.Retired:
			reg = policy.Retired
		case p.Retiring:
			reg = policy.Retiring
		}
		st.Pools = append(st.Pools, policy.Pool{
			ID: actor, Reg: reg, Live: !pods.Dark(actor), Running: pods.PodRunning(actor),
		})
	}
	slices.SortFunc(st.Pools, func(a, b policy.Pool) int { return int(a.ID) - int(b.ID) })
	return st
}

// schedule converts a snapshot, whose credentials the book names.
func schedule(b *Book, s Snapshot) policy.Schedule {
	var out policy.Schedule
	for _, e := range s {
		out = append(out, policy.Delegation{
			Wallet: b.stake[e.Cred], Pool: b.pools[e.Pool], Stake: e.Stake,
		})
	}
	return out
}

var _ agenda.Governance = (*View)(nil)
