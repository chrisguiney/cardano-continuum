package engine

import (
	"github.com/chrisguiney/cardano-continuum/internal/adversary"
	"github.com/chrisguiney/cardano-continuum/internal/agenda"
	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
)

// This file keeps the engine's chain.View: the delegation, pool, DRep and
// governance state the agendas and the stake-floor guard read. It is fed
// the same blocks and rollbacks as the planner's ledger, so its settled
// point is the engine's confirmed point, and its state at that point rides
// in the snapshot.

// view adapts the engine's own chain view to agenda.View and the optional
// Governance. Decisions read only this, never the live chain, so replay
// sees the same world.
type view struct{ e *Engine }

func (v view) Balance(id population.ActorID) profile.Lovelace {
	return profile.Lovelace(v.e.Spendable(uint64(id)))
}
func (v view) Pools() []agenda.PoolInfo { return v.e.win.View().Pools() }
func (v view) Epoch() uint64            { return v.e.cfg.Slots.EpochAt(v.e.clk.Now()) }
func (v view) StakeRegistered(id population.ActorID) bool {
	return v.e.win.View().StakeRegistered(id)
}
func (v view) DelegatedTo(id population.ActorID) (population.ActorID, bool) {
	return v.e.win.View().DelegatedTo(id)
}
func (v view) PoolRegistered(id population.ActorID) bool { return v.e.win.View().PoolRegistered(id) }

// Budgets implements adversary.Sight: the run's execution limits.
func (v view) Budgets() adversary.Budgets {
	return adversary.Budgets{Tx: v.e.cfg.Build.MaxTxExUnits, Block: v.e.cfg.Build.MaxBlockExUnits}
}

// OpenProposals implements agenda.Governance.
func (v view) OpenProposals() []agenda.ProposalInfo { return v.e.win.View().OpenProposals() }

// Voted implements agenda.Governance.
func (v view) Voted(a population.ActorID, p agenda.ProposalID) bool {
	return v.e.win.View().Voted(a, p)
}

// DRepRegistered implements agenda.Governance.
func (v view) DRepRegistered(a population.ActorID) bool { return v.e.win.View().DRepRegistered(a) }

// DReps implements agenda.Governance.
func (v view) DReps() []population.ActorID { return v.e.win.View().DReps() }

// VoteDelegated implements agenda.Governance.
func (v view) VoteDelegated(a population.ActorID) bool { return v.e.win.View().VoteDelegated(a) }

var (
	_ agenda.Governance = view{}
	_ agenda.Market     = view{}
	_ agenda.Floor      = view{}
	_ agenda.Pods       = view{}
)

// Burst implements agenda.Market.
func (v view) Burst() float64 { return v.e.pc.Burst(v.e.clk.Now()) }

// Forging implements agenda.Floor.
func (v view) Forging(pool population.ActorID) bool { return v.e.fl.Forging(pool) }

// Allows implements agenda.Floor.
func (v view) Allows(in agenda.Intent) (bool, string) {
	vd := v.e.fl.Allows(v.e.clk.Now(), in)
	return vd.OK, vd.Why
}

// Dark implements agenda.Pods.
func (v view) Dark(pool population.ActorID) bool { return v.e.fl.Dark(pool) }

// PodRunning implements agenda.Pods.
func (v view) PodRunning(pool population.ActorID) bool { return v.e.fl.PodRunning(pool) }

// Shared implements adversary.Sight.
func (v view) Shared() []adversary.Locked { return v.e.adv.Shared() }

// Payees implements adversary.Sight.
func (v view) Payees() []population.ActorID { return v.e.adv.Payees() }

// Victims implements adversary.Sight.
func (v view) Victims() []adversary.Coin { return v.e.adv.Victims() }

// Remaining implements adversary.View.
func (v view) Remaining(id population.ActorID) uint64 { return v.e.adv.Remaining(id) }

var _ adversary.View = view{}
