package engine

import (
	"context"

	"github.com/blinklabs-io/gouroboros/ledger/common"
	"strconv"
	"strings"

	"github.com/chrisguiney/cardano-continuum/internal/agenda"
	"github.com/chrisguiney/cardano-continuum/internal/build"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/clock"
	"github.com/chrisguiney/cardano-continuum/internal/keys"
	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
	"github.com/chrisguiney/cardano-continuum/internal/upgrade"
)

// This file applies Control inputs: the web UI's steering (internal/httpapi
// appends them to the log), replayed like any other input. A control
// changes engine state and is acknowledged with a Controlled decision.

// control is httpapi.Control as the log carries it. The CBOR keys are the
// log format; the engine cannot import httpapi, which reads engine bodies.
type control struct {
	RequestID string             `cbor:"1,keyasint"`
	Op        string             `cbor:"2,keyasint"`
	K         uint32             `cbor:"3,keyasint,omitempty"`
	Weights   map[string]float64 `cbor:"4,keyasint,omitempty"`
	Scenario  string             `cbor:"5,keyasint,omitempty"`
}

// Scenarios an inject control names.
const (
	// scenarioPropose: the richest actor with a registered stake key
	// proposes an info action.
	scenarioPropose = "info_proposal"
	// scenarioGoDark: "go_dark" or "go_dark:<pool actor>" asks for a pool to
	// go dark, through the stake-floor guard. Without an actor it is the
	// lowest-numbered pool actor that is live.
	scenarioGoDark = "go_dark"
	// scenarioHotKeys: the richest actor pays to authorise a hot key for
	// every genesis committee member.
	scenarioHotKeys = "hot_keys"
	// scenarioSPOProbe: the richest registered pool votes on an open
	// proposal stake pools may not vote on. The ledger must refuse it
	// (DisallowedVoters); a node that accepts it is a fatal finding.
	scenarioSPOProbe = "spo_vote_probe"
	// scenarioUpgrade: "upgrade:<image>" or "upgrade:<epochs>:<image>"
	// starts an upgrade wave over the pools, adopting along a logistic
	// curve over that many epochs (one by default). An image reference
	// holds colons of its own, so the epochs come first.
	scenarioUpgrade = "upgrade"
)

// upgradeSteepness is a wave's logistic half-width in e-folds.
const upgradeSteepness = 4

func (e *Engine) controlInput(ctx context.Context, at sched.Time, c control) error {
	why := ""
	switch c.Op {
	case "pause":
		e.paused = true
	case "resume":
		e.paused = false
	case "set_k":
		if c.K == 0 {
			why = "k must be positive"
			break
		}
		e.rateK = c.K
	case "set_weights":
		e.weights = map[profile.IntentKind]float64{}
		for k, w := range c.Weights {
			e.weights[profile.IntentKind(k)] = w
		}
	case "inject":
		var err error
		if why, err = e.inject(ctx, at, c.Scenario); err != nil {
			return err
		}
	default:
		why = "unknown op"
	}
	e.jr.Emit(at, KindControlled, 0, Controlled{RequestID: c.RequestID, Op: c.Op, Scenario: c.Scenario, Why: why})
	return nil
}

// inject starts a scenario, or says why it cannot.
func (e *Engine) inject(ctx context.Context, at sched.Time, s string) (string, error) {
	name, arg, _ := strings.Cut(s, ":")
	switch name {
	case scenarioPropose:
		return e.injectPropose(ctx, at)
	case scenarioGoDark:
		return e.injectGoDark(at, arg), nil
	case scenarioHotKeys:
		return e.injectHotKeys(ctx, at)
	case scenarioSPOProbe:
		return e.injectSPOProbe(ctx, at)
	case scenarioUpgrade:
		return e.startWave(at, arg), nil
	}
	return "unknown scenario", nil
}

func (e *Engine) injectPropose(ctx context.Context, at sched.Time) (string, error) {
	id, ok := e.richest(func(id population.ActorID) bool { return e.win.View().StakeRegistered(id) })
	if !ok {
		return "no actor with a registered stake key", nil
	}
	return e.start(ctx, at, &pendingRec{Actor: uint64(id), Kind: string(profile.IntentPropose)})
}

func (e *Engine) injectSPOProbe(ctx context.Context, at sched.Time) (string, error) {
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
	ref, err := parseProposal(target.ID)
	if err != nil {
		return err.Error(), nil
	}
	id, ok := e.richest(func(id population.ActorID) bool {
		_, pool := e.co.Ring.PoolOf(id)
		return pool && e.win.View().PoolRegistered(id)
	})
	if !ok {
		return "no registered pool", nil
	}
	voter, _ := e.voterOf(id)
	return e.start(ctx, at, &pendingRec{Actor: uint64(id), Kind: string(profile.IntentVote), Action: &ref,
		Choice: common.GovVoteYes, Power: e.win.View().VotingPower(voter), Probe: true})
}

func (e *Engine) injectGoDark(at sched.Time, arg string) string {
	var pool population.ActorID
	if arg != "" {
		n, err := strconv.ParseUint(arg, 10, 32)
		if err != nil {
			return "malformed pool actor " + arg
		}
		pool = population.ActorID(n)
	}
	for _, id := range e.co.Ring.PoolIDs() {
		if pool == 0 && !e.dark[id] {
			pool = id
		}
	}
	if _, ok := e.co.Ring.PoolOf(pool); !ok {
		return "no such pool actor"
	}
	in := agenda.Intent{Kind: agenda.GoDark, Actor: pool}
	if !e.checkFloor(at, in) {
		return "refused by the stake floor"
	}
	e.act1Pool(at, in)
	return ""
}

func (e *Engine) injectHotKeys(ctx context.Context, at sched.Time) (string, error) {
	id, ok := e.richest(func(population.ActorID) bool { return true })
	if !ok {
		return "no actor to pay", nil
	}
	for i := range e.cfg.Committee {
		why, err := e.start(ctx, at, &pendingRec{Actor: uint64(id), Kind: hotKind, To: uint64(i)})
		if err != nil || why != "" {
			return why, err
		}
	}
	return "", nil
}

// richest is the wallet or pool actor with the most to spend that passes
// ok, the lowest ID on a tie.
func (e *Engine) richest(ok func(population.ActorID) bool) (population.ActorID, bool) {
	var best population.ActorID
	var most uint64
	for _, id := range e.co.IDs() {
		if a, _ := e.co.Get(id); a.State == population.Abandoned || !ok(id) {
			continue
		}
		if s := e.Spendable(uint64(id)); s > most {
			best, most = id, s
		}
	}
	return best, most > 0
}

// startWave plans an upgrade wave from at: every pool actor still
// registered adopts image at a time internal/upgrade draws.
func (e *Engine) startWave(at sched.Time, arg string) string {
	image, n := arg, uint64(1)
	if head, rest, ok := strings.Cut(arg, ":"); ok {
		if v, err := strconv.ParseUint(head, 10, 16); err == nil {
			image, n = rest, v
		}
	}
	if image == "" || n == 0 {
		return "need an image and a positive number of epochs"
	}
	c := upgrade.Curve{Start: at, End: at + sched.Time(n)*e.cfg.Slots.EpochStart(1), Steepness: upgradeSteepness}
	if err := c.Validate(); err != nil {
		return err.Error()
	}
	var pools []upgrade.Pool
	for _, id := range e.co.Ring.PoolIDs() {
		if a, _ := e.co.Get(id); a.State == population.Retired {
			continue
		}
		pools = append(pools, upgrade.Pool{ID: strconv.FormatUint(uint64(id), 10), Eagerness: e.co.Rec(id).Prof.Eager()})
	}
	e.waves++
	src := e.cfg.Seed.Derive("upgrade", strconv.FormatUint(e.waves, 10)).Rand()
	for _, in := range upgrade.Plan(c, image, pools, src) {
		e.adoptions = append(e.adoptions, adoption{At: in.At, Pool: in.Pool, Ref: in.Ref})
		e.clk.Push(in.At, clock.Event{Kind: clock.Adopt, Intent: uint64(len(e.adoptions) - 1)})
	}
	return ""
}

// adopt records one pool's adoption: the image the plan now gives it.
func (e *Engine) adopt(at sched.Time, i uint64) {
	a := e.adoptions[i]
	if a.Done {
		return
	}
	e.adoptions[i].Done = true
	n, _ := strconv.ParseUint(a.Pool, 10, 32)
	pool := population.ActorID(n)
	e.images[pool] = a.Ref
	e.jr.Emit(at, KindImageChange, pool, ImageChange{Ref: a.Ref})
}

// adoption is one queued ImageChange of a wave.
type adoption struct {
	At   sched.Time `cbor:"1,keyasint"`
	Pool string     `cbor:"2,keyasint"`
	Ref  string     `cbor:"3,keyasint"`
	Done bool       `cbor:"4,keyasint,omitempty"`
}

func mapKeys[V any](m map[population.ActorID]V) func(func(population.ActorID) bool) {
	return func(yield func(population.ActorID) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

// hotKind authorises committee member To's hot key (build.AuthorizeHot),
// paid by the actor. The member's keys are the run seed's own, as
// internal/genesis seats them.
const hotKind = "authorize_hot"

func buildHot(ctx context.Context, e *Engine, w keys.Wallet, sp build.Spend, p *pendingRec) (build.Built, error) {
	cold, hot := e.co.Ring.Committee(uint32(p.To))
	return e.builder.Gov().AuthorizeHot(ctx, w, build.HotAuth{Spend: sp, Cold: cold, Hot: hot.KeyHash()})
}

// PoolPlan is what the engine wants of one pool actor's ContinuumPool.
type PoolPlan struct {
	Actor uint64
	// Genesis is the genesis pool index, or -1 for a pool the engine
	// registered.
	Genesis int
	Dark    bool
	// Image is the image an upgrade wave gave the pool; empty is the
	// network's default.
	Image string
}

// Pools is the pool plan, by actor. A live run patches each ContinuumPool
// from it; the fake has nothing to patch.
func (e *Engine) Pools() []PoolPlan {
	var out []PoolPlan
	for _, id := range e.co.Ring.PoolIDs() {
		p := PoolPlan{Actor: uint64(id), Genesis: -1, Dark: e.dark[id], Image: e.images[id]}
		if g, ok := e.co.Ring.Gen.Of(id); ok {
			p.Genesis = g.Index
		}
		out = append(out, p)
	}
	return out
}
