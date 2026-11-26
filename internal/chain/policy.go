package chain

import (
	"slices"

	"github.com/chrisguiney/cardano-continuum/internal/agenda"
	"github.com/chrisguiney/cardano-continuum/internal/policy"
)

// PolicyState builds the stake-floor guard's input (internal/policy,
// models/specs/stake-floor) from the view.
//
// epoch is the current epoch by slot time, which the view cannot know: its
// own epoch is that of the tip. Pods and inflight come from the engine: the
// plan and observed pod health, and the transactions submitted but neither
// confirmed nor expired.
//
// Label, Set and Mark come from the tip, so they agree with each other: Set
// and Mark are what the last transition on the tip's chain took. Deleg and
// Pools come from the settled point, since the contract counts a delegation
// or certificate as confirmed only K deep (rule 3, engine-recovery design
// A); a certificate still above it is neither in Deleg nor in Pools. A pool
// the view has never seen registered is left out.
func (v *View) PolicyState(epoch uint64, pods agenda.Pods, inflight []policy.Intent) policy.State {
	c := v.Confirmed()
	st := policy.State{
		Epoch:    epoch,
		Label:    v.ep.label,
		Set:      v.schedule(v.ep.set),
		Mark:     v.schedule(v.ep.mark),
		Deleg:    c.delegation(),
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
func (v *View) schedule(s Snapshot) policy.Schedule {
	var out policy.Schedule
	for _, e := range s {
		out = append(out, policy.Delegation{
			Wallet: v.cfg.Book.stake[e.Cred], Pool: v.cfg.Book.pools[e.Pool], Stake: e.Stake,
		})
	}
	return out
}

// delegation is the confirmed delegation: every registered credential, with
// pool zero for one delegated nowhere the book names.
func (v *View) delegation() policy.Schedule {
	stake := v.credStake()
	var out policy.Schedule
	for _, cred := range sortedKeys(v.stake) {
		s := v.stake[cred]
		if !s.Registered {
			continue
		}
		d := policy.Delegation{Wallet: v.cfg.Book.stake[cred], Stake: stake[cred]}
		if s.Delegated {
			d.Pool = v.cfg.Book.pools[s.Pool]
		}
		out = append(out, d)
	}
	return out
}

var _ agenda.Governance = (*View)(nil)
