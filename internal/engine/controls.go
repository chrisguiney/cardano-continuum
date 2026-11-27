package engine

import (
	"context"
	"github.com/chrisguiney/cardano-continuum/internal/rng"
	"github.com/chrisguiney/cardano-continuum/internal/upgrade"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/blinklabs-io/gouroboros/ledger/common"

	"github.com/chrisguiney/cardano-continuum/internal/agenda"
	"github.com/chrisguiney/cardano-continuum/internal/build"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/intent"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/wire"
	"github.com/chrisguiney/cardano-continuum/internal/keys"
	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
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

// scenarioRoute is the engine's side of an inject control: the intents and
// the stake floor the scenarios start.
type scenarioRoute struct{ e *Engine }

// Propose implements scenarios.
func (r scenarioRoute) Propose(ctx context.Context, at sched.Time) (string, error) {
	e := r.e
	id, ok := r.richest(func(id population.ActorID) bool { return e.win.View().StakeRegistered(id) })
	if !ok {
		return "no actor with a registered stake key", nil
	}
	return e.start(ctx, at, &wire.Pending{Actor: uint64(id), Args: wire.Args{Kind: string(profile.IntentPropose)}})
}

// SPOProbe implements scenarios.
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
	return e.start(ctx, at, &wire.Pending{Actor: uint64(id), Args: wire.Args{Kind: string(profile.IntentVote)}, Gov: wire.Gov{Action: &ref, Choice: common.GovVoteYes, Power: e.win.View().VotingPower(voter), Probe: true}})
}

// GoDark implements scenarios.
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

// HotKeys implements scenarios.
func (r scenarioRoute) HotKeys(ctx context.Context, at sched.Time) (string, error) {
	e := r.e
	id, ok := r.richest(func(population.ActorID) bool { return true })
	if !ok {
		return "no actor to pay", nil
	}
	for i := range e.cfg.Committee {
		why, err := e.start(ctx, at, &wire.Pending{Actor: uint64(id), Args: wire.Args{Kind: hotKind, To: uint64(i)}})
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
		if s := e.Spendable(uint64(id)); s > most {
			best, most = id, s
		}
	}
	return best, most > 0
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

func buildHot(ctx context.Context, e *Engine, w keys.Wallet, sp build.Spend, p *wire.Pending) (build.Built, error) {
	cold, hot := e.co.Ring.Committee(uint32(p.To))
	return e.builder.Gov().AuthorizeHot(ctx, w, build.HotAuth{Spend: sp, Cold: cold, Hot: hot.KeyHash()})
}

// steerRing is the cohort's pool actors and the stake floor's plan as the
// steering reads them.
type steerRing struct{ e *Engine }

// PoolIDs implements steerPools.
func (r steerRing) PoolIDs() []population.ActorID { return r.e.co.Ring.PoolIDs() }

// Genesis implements steerPools.
func (r steerRing) Genesis(id population.ActorID) (int, bool) {
	g, ok := r.e.co.Ring.Gen.Of(id)
	return g.Index, ok
}

// Retired implements steerPools.
func (r steerRing) Retired(id population.ActorID) bool {
	a, _ := r.e.co.Get(id)
	return a.State == population.Retired
}

// Eager implements steerPools.
func (r steerRing) Eager(id population.ActorID) float64 { return r.e.co.Rec(id).Prof.Eager() }

// Dark implements steerPools.
func (r steerRing) Dark(id population.ActorID) bool { return r.e.fl.Dark(id) }

// Pools is the pool plan, by actor. A live run patches each ContinuumPool
// from it; the fake has nothing to patch.
func (e *Engine) Pools() []PoolPlan { return e.st.Pools() }

// steering is the operator's hand on the run: pause, rate, intent weights
// and upgrade waves. The pace reads its knobs; the web UI's controls change
// them, replayed from the log like any other input.

// scenarios routes an inject control to the intent that carries it out.
type scenarios interface {
	// Propose has the richest registered actor propose an info action.
	Propose(ctx context.Context, at sched.Time) (string, error)
	// SPOProbe has the richest registered pool vote where pools may not.
	SPOProbe(ctx context.Context, at sched.Time) (string, error)
	// GoDark asks the stake floor to take a pool dark (arg names the pool,
	// empty for the first live one).
	GoDark(at sched.Time, arg string) string
	// HotKeys has the richest actor authorise every committee hot key.
	HotKeys(ctx context.Context, at sched.Time) (string, error)
}

// steerPools is the pool actors as an upgrade wave and the pool plan read
// them.
type steerPools interface {
	// PoolIDs are the pool actors, ascending.
	PoolIDs() []population.ActorID
	// Genesis is the pool's genesis index, if it is a genesis pool.
	Genesis(id population.ActorID) (int, bool)
	// Retired reports that the actor has retired.
	Retired(id population.ActorID) bool
	// Eager is the pool operator's eagerness to adopt.
	Eager(id population.ActorID) float64
	// Dark reports that the plan wants the pool not forging.
	Dark(id population.ActorID) bool
}

// steerClock queues an adoption event.
type steerClock interface {
	Adopt(at sched.Time, i uint64)
}

// steerJournal is the decision log of the input being handled.
type steerJournal interface {
	Emit(at sched.Time, kind string, a population.ActorID, body any)
}

// steerSpec is what a steering is built from.
type steerSpec struct {
	Seed  rng.Seed
	Slots sched.Slots
}

// steerEnv is what steering shares with the rest of the engine.
type steerEnv struct {
	Scenarios scenarios
	Pools     steerPools
	Clock     steerClock
	Journal   steerJournal
}

// steering owns the run's controls and upgrade waves.
type steering struct {
	spec steerSpec
	env  steerEnv

	// paused stops the agendas; rateK multiplies every actor's rate (zero
	// is 1); weights override the profiles' intent weights.
	paused  bool
	rateK   uint32
	weights map[profile.IntentKind]float64
	// waves counts upgrade waves; adoptions are their queued image
	// changes and images the plan they built, by pool actor.
	waves     uint64
	adoptions []adoption
	images    map[population.ActorID]string
}

func newSteering(sp steerSpec, env steerEnv) *steering {
	return &steering{spec: sp, env: env, images: map[population.ActorID]string{}}
}

// Paused reports that the agendas are stopped.
func (s *steering) Paused() bool { return s.paused }

// RateK is the rate multiplier; less than one reads as one.
func (s *steering) RateK() uint32 { return s.rateK }

// Weights are the intent weights the controls override, by kind.
func (s *steering) Weights() map[profile.IntentKind]float64 { return s.weights }

// Control applies one control input and acknowledges it with a Controlled
// decision.
func (s *steering) Control(ctx context.Context, at sched.Time, c control) error {
	why := ""
	switch c.Op {
	case "pause":
		s.paused = true
	case "resume":
		s.paused = false
	case "set_k":
		if c.K == 0 {
			why = "k must be positive"
			break
		}
		s.rateK = c.K
	case "set_weights":
		s.weights = map[profile.IntentKind]float64{}
		for k, w := range c.Weights {
			s.weights[profile.IntentKind(k)] = w
		}
	case "inject":
		var err error
		if why, err = s.inject(ctx, at, c.Scenario); err != nil {
			return err
		}
	default:
		why = "unknown op"
	}
	s.env.Journal.Emit(at, wire.KindControlled, 0, wire.Controlled{RequestID: c.RequestID, Op: c.Op, Scenario: c.Scenario, Why: why})
	return nil
}

// inject starts a scenario, or says why it cannot.
func (s *steering) inject(ctx context.Context, at sched.Time, sc string) (string, error) {
	name, arg, _ := strings.Cut(sc, ":")
	switch name {
	case scenarioPropose:
		return s.env.Scenarios.Propose(ctx, at)
	case scenarioGoDark:
		return s.env.Scenarios.GoDark(at, arg), nil
	case scenarioHotKeys:
		return s.env.Scenarios.HotKeys(ctx, at)
	case scenarioSPOProbe:
		return s.env.Scenarios.SPOProbe(ctx, at)
	case scenarioUpgrade:
		return s.startWave(at, arg), nil
	}
	return "unknown scenario", nil
}

// startWave plans an upgrade wave from at: every pool actor still
// registered adopts image at a time internal/upgrade draws.
func (s *steering) startWave(at sched.Time, arg string) string {
	image, n := arg, uint64(1)
	if head, rest, ok := strings.Cut(arg, ":"); ok {
		if v, err := strconv.ParseUint(head, 10, 16); err == nil {
			image, n = rest, v
		}
	}
	if image == "" || n == 0 {
		return "need an image and a positive number of epochs"
	}
	c := upgrade.Curve{Start: at, End: at + sched.Time(n)*s.spec.Slots.EpochStart(1), Steepness: upgradeSteepness}
	if err := c.Validate(); err != nil {
		return err.Error()
	}
	var pools []upgrade.Pool
	for _, id := range s.env.Pools.PoolIDs() {
		if s.env.Pools.Retired(id) {
			continue
		}
		pools = append(pools, upgrade.Pool{ID: strconv.FormatUint(uint64(id), 10), Eagerness: s.env.Pools.Eager(id)})
	}
	s.waves++
	src := s.spec.Seed.Derive("upgrade", strconv.FormatUint(s.waves, 10)).Rand()
	for _, in := range upgrade.Plan(c, image, pools, src) {
		s.adoptions = append(s.adoptions, adoption{At: in.At, Pool: in.Pool, Ref: in.Ref})
		s.env.Clock.Adopt(in.At, uint64(len(s.adoptions)-1))
	}
	return ""
}

// Adopt records one pool's adoption: the image the plan now gives it.
func (s *steering) Adopt(at sched.Time, i uint64) {
	a := s.adoptions[i]
	if a.Done {
		return
	}
	s.adoptions[i].Done = true
	n, _ := strconv.ParseUint(a.Pool, 10, 32)
	pool := population.ActorID(n)
	s.images[pool] = a.Ref
	s.env.Journal.Emit(at, wire.KindImageChange, pool, wire.ImageChange{Ref: a.Ref})
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
func (s *steering) Pools() []PoolPlan {
	var out []PoolPlan
	for _, id := range s.env.Pools.PoolIDs() {
		p := PoolPlan{Actor: uint64(id), Genesis: -1, Dark: s.env.Pools.Dark(id), Image: s.images[id]}
		if g, ok := s.env.Pools.Genesis(id); ok {
			p.Genesis = g
		}
		out = append(out, p)
	}
	return out
}

// steerSaved is steering's state in a snapshot.
type steerSaved struct {
	Paused    bool
	RateK     uint32
	Waves     uint64
	Adoptions []adoption
	Weights   map[profile.IntentKind]float64
	Images    map[population.ActorID]string
}

// Save returns the state a snapshot keeps.
func (s *steering) Save() steerSaved {
	return steerSaved{Paused: s.paused, RateK: s.rateK, Waves: s.waves, Adoptions: slices.Clone(s.adoptions), Weights: s.weights, Images: s.images}
}

// Load restores a Save's state and queues the adoptions not yet done.
func (s *steering) Load(v steerSaved) {
	s.paused, s.rateK, s.waves, s.adoptions = v.Paused, v.RateK, v.Waves, v.Adoptions
	if len(v.Weights) > 0 {
		s.weights = maps.Clone(v.Weights)
	}
	maps.Copy(s.images, v.Images)
	for i, a := range s.adoptions {
		if !a.Done {
			s.env.Clock.Adopt(a.At, uint64(i))
		}
	}
}
