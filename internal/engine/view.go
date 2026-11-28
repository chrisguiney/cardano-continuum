package engine

import (
	"github.com/chrisguiney/cardano-continuum/internal/adversary"
	"github.com/chrisguiney/cardano-continuum/internal/agenda"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/intent"
	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
	"github.com/chrisguiney/cardano-continuum/internal/protocol"
)

// view adapts the engine's components to agenda.View and its optional
// capabilities, and to adversary.View. Decisions read only this, never the
// live chain, so replay sees the same world. The chain view it reads is fed
// the same blocks and rollbacks as the planner's ledger, so its settled
// point is the engine's confirmed point.
//
// The agendas find the optional capabilities with type assertions, so each
// is also asserted below at compile time: a lost method fails the build
// instead of silently turning an agenda off.
type view struct {
	e *Engine
	govView
	advView
}

func viewOf(e *Engine) view { return view{e: e, govView: govView{e}, advView: advView{e}} }

var (
	_ agenda.View       = view{}
	_ agenda.Governance = view{}
	_ agenda.Market     = view{}
	_ agenda.Floor      = view{}
	_ agenda.Pods       = view{}
	_ agenda.Deposits   = view{}
	_ agenda.Committee  = view{}
	_ agenda.Trading    = view{}
	_ adversary.View    = view{}
)

func (v view) Balance(id population.ActorID) profile.Lovelace {
	return profile.Lovelace(v.e.pay.Spendable(uint64(id)))
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

// Deposit implements agenda.Deposits from the builder's parameters, which
// are the chain's.
func (v view) Deposit(k profile.IntentKind) (profile.Lovelace, bool) {
	var d uint64
	switch k {
	case profile.IntentRegisterStake:
		d = v.e.cfg.Build.KeyDeposit
	case profile.IntentRegisterPool:
		d = v.e.cfg.Build.PoolDeposit
	case profile.IntentRegisterDRep:
		d = v.e.cfg.Build.DRepDeposit
	default:
		return 0, false
	}
	return profile.Lovelace(d), true
}

// Position implements agenda.Trading.
func (v view) Position(id population.ActorID) (agenda.Position, bool) {
	now := v.e.clk.Now()
	pos, ok := v.e.co.Desk.Position(id, now)
	pos.Dry = ok && v.e.co.Desk.Dry(id, now, func(x population.ActorID) uint64 { return v.e.pay.Spendable(uint64(x)) }, intent.MinTrade+feeHeadroom)
	return pos, ok
}

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

// govView is the Conway governance state of the chain view, as
// agenda.Governance reads it.
type govView struct{ e *Engine }

// OpenProposals implements agenda.Governance.
func (v govView) OpenProposals() []agenda.ProposalInfo { return v.e.win.View().OpenProposals() }

// Voted implements agenda.Governance.
func (v govView) Voted(a population.ActorID, p agenda.ProposalID) bool {
	return v.e.win.View().Voted(a, p)
}

// DRepRegistered implements agenda.Governance.
func (v govView) DRepRegistered(a population.ActorID) bool { return v.e.win.View().DRepRegistered(a) }

// DReps implements agenda.Governance.
func (v govView) DReps() []population.ActorID { return v.e.win.View().DReps() }

// VoteDelegated implements agenda.Governance.
func (v govView) VoteDelegated(a population.ActorID) bool { return v.e.win.View().VoteDelegated(a) }

// Seats implements agenda.Committee: the genesis committee's seats.
func (v govView) Seats() int { return v.e.cfg.Committee }

// CommitteeVoted implements agenda.Committee.
func (v govView) CommitteeVoted(seat int, p agenda.ProposalID) bool {
	return v.e.win.View().CommitteeVoted(seat, p)
}

// HotAuthorized implements agenda.Committee.
func (v govView) HotAuthorized(seat int) bool { return v.e.win.View().HotAuthorized(seat) }

// JointAuth implements agenda.Committee from the protocol version in force
// at the tip.
func (v govView) JointAuth() bool {
	cv := v.e.win.View()
	return cv.VersionAt(cv.Epoch()).Major >= protocol.VanRossem
}

// advView is the adversaries' side of the view: adversary.Sight and the
// cost ceiling left.
type advView struct{ e *Engine }

// Budgets implements adversary.Sight: the run's execution limits.
func (v advView) Budgets() adversary.Budgets {
	return adversary.Budgets{Tx: v.e.cfg.Build.MaxTxExUnits, Block: v.e.cfg.Build.MaxBlockExUnits}
}

// Shared implements adversary.Sight.
func (v advView) Shared() []adversary.Locked { return v.e.adv.Shared() }

// Payees implements adversary.Sight.
func (v advView) Payees() []population.ActorID { return v.e.adv.Payees() }

// Victims implements adversary.Sight.
func (v advView) Victims() []adversary.Coin { return v.e.adv.Victims() }

// Remaining implements adversary.View.
func (v advView) Remaining(id population.ActorID) uint64 { return v.e.adv.Remaining(id) }
