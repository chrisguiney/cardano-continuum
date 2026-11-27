package engine

import (
	"context"

	"github.com/blinklabs-io/gouroboros/ledger/common"

	"strconv"

	"github.com/chrisguiney/cardano-continuum/internal/agenda"
	"github.com/chrisguiney/cardano-continuum/internal/build"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/intent"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/steer"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/wire"
	"github.com/chrisguiney/cardano-continuum/internal/keys"
	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

// This file routes the steer component (internal/engine/internal/steer) into
// the engine: the inject scenarios that start an intent or ask the stake
// floor for a pool, and the pool actors steer reads.

// control is a control input as the log carries it.
type control = wire.Control

// PoolPlan is what the engine wants of one pool actor's ContinuumPool.
type PoolPlan = steer.PoolPlan

// ScenarioInfo describes one scenario an inject control may name.
type ScenarioInfo = steer.ScenarioInfo

// Scenarios lists every scenario an inject control may name, in display
// order.
func Scenarios() []ScenarioInfo { return steer.Catalog() }

// scenarioRoute is the engine's side of an inject control: the intents and
// the stake floor the scenarios start.
type scenarioRoute struct{ e *Engine }

// Propose implements steer.Scenarios.
func (r scenarioRoute) Propose(ctx context.Context, at sched.Time) (string, error) {
	e := r.e
	id, ok := r.richest(func(id population.ActorID) bool { return e.win.View().StakeRegistered(id) })
	if !ok {
		return "no actor with a registered stake key", nil
	}
	return starter{e}.start(ctx, at, &wire.Pending{Actor: uint64(id), Args: wire.Args{Kind: string(profile.IntentPropose)}})
}

// SPOProbe implements steer.Scenarios.
func (r scenarioRoute) SPOProbe(ctx context.Context, at sched.Time) (string, error) {
	e := r.e
	var target agenda.ProposalInfo
	for _, p := range e.win.View().OpenProposals() {
		if !p.Pools && p.Type != "" {
			target = p
			break
		}
	}
	if target.ID == "" {
		return "no open proposal stake pools may not vote on", nil
	}
	ref, err := intent.ParseProposal(target.ID)
	if err != nil {
		return err.Error(), nil
	}
	id, ok := r.richest(func(id population.ActorID) bool {
		_, pool := e.co.Ring.PoolOf(id)
		return pool && e.win.View().PoolRegistered(id)
	})
	if !ok {
		return "no registered pool", nil
	}
	voter, _ := intent.Voter(e.co.Ring, id)
	return starter{e}.start(ctx, at, &wire.Pending{Actor: uint64(id), Args: wire.Args{Kind: string(profile.IntentVote)}, Gov: wire.Gov{Action: &ref, Choice: common.GovVoteYes, Power: e.win.View().VotingPower(voter), Probe: true}})
}

// GoDark implements steer.Scenarios.
func (r scenarioRoute) GoDark(at sched.Time, arg string) string {
	e := r.e
	var pool population.ActorID
	if arg != "" {
		n, err := strconv.ParseUint(arg, 10, 32)
		if err != nil {
			return "malformed pool actor " + arg
		}
		pool = population.ActorID(n)
	}
	for _, id := range e.co.Ring.PoolIDs() {
		if pool == 0 && !e.fl.Dark(id) {
			pool = id
		}
	}
	if _, ok := e.co.Ring.PoolOf(pool); !ok {
		return "no such pool actor"
	}
	in := agenda.Intent{Kind: agenda.GoDark, Actor: pool}
	if !e.fl.Check(at, in) {
		return "refused by the stake floor"
	}
	e.fl.Take(at, in)
	return ""
}

// HotKeys implements steer.Scenarios.
func (r scenarioRoute) HotKeys(ctx context.Context, at sched.Time) (string, error) {
	e := r.e
	id, ok := r.richest(func(population.ActorID) bool { return true })
	if !ok {
		return "no actor to pay", nil
	}
	for i := range e.cfg.Committee {
		why, err := (starter{e}).start(ctx, at, &wire.Pending{Actor: uint64(id), Args: wire.Args{Kind: hotKind, To: uint64(i)}})
		if err != nil || why != "" {
			return why, err
		}
	}
	return "", nil
}

// richest is the wallet or pool actor with the most to spend that passes
// ok, the lowest ID on a tie.
func (r scenarioRoute) richest(ok func(population.ActorID) bool) (population.ActorID, bool) {
	e := r.e
	var best population.ActorID
	var most uint64
	for _, id := range e.co.IDs() {
		if a, _ := e.co.Get(id); a.State == population.Abandoned || !ok(id) {
			continue
		}
		if s := e.pay.Spendable(uint64(id)); s > most {
			best, most = id, s
		}
	}
	return best, most > 0
}

// hotKind authorises committee member To's hot key (build.AuthorizeHot),
// paid by the actor. The member's keys are the run seed's own, as
// internal/genesis seats them.
const hotKind = "authorize_hot"

func buildHot(ctx context.Context, e *Engine, w keys.Wallet, sp build.Spend, p *wire.Pending) (build.Built, error) {
	cold, hot := e.co.Ring.Committee(uint32(p.To))
	return e.builder.Gov().AuthorizeHot(ctx, w, build.HotAuth{Spend: sp, Cold: cold, Hot: hot.KeyHash()})
}

// steerRing is the cohort's pool actors and the stake floor's plan as the
// steering reads them.
type steerRing struct{ e *Engine }

// PoolIDs implements steer.Roster.
func (r steerRing) PoolIDs() []population.ActorID { return r.e.co.Ring.PoolIDs() }

// Genesis implements steer.Roster.
func (r steerRing) Genesis(id population.ActorID) (int, bool) {
	g, ok := r.e.co.Ring.Gen.Of(id)
	return g.Index, ok
}

// Retired implements steer.Roster.
func (r steerRing) Retired(id population.ActorID) bool {
	a, _ := r.e.co.Get(id)
	return a.State == population.Retired
}

// Eager implements steer.Roster.
func (r steerRing) Eager(id population.ActorID) float64 { return r.e.co.Rec(id).Prof.Eager() }

// Dark implements steer.Roster.
func (r steerRing) Dark(id population.ActorID) bool { return r.e.fl.Dark(id) }
