// Package steer is the operator's hand on the run: pause, rate, intent
// weights and upgrade waves. The pace reads its knobs; the web UI's controls
// change them, replayed from the log like any other input, and each is
// acknowledged with a Controlled decision.
//
// An inject control names a scenario; the ones that start an intent (a
// proposal, a vote, hot-key authorisations) or ask the stake floor to take a
// pool dark are routed back through Scenarios, which the composite
// implements. An upgrade wave is planned here with internal/upgrade and
// queued as adoption events on the clock.
package steer

import (
	"context"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/wire"
	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
	"github.com/chrisguiney/cardano-continuum/internal/rng"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
	"github.com/chrisguiney/cardano-continuum/internal/upgrade"
)

// Scenarios an inject control names.
const (
	// ScenarioPropose: the network proposer (the richest actor with a
	// registered stake key when there is none)
	// proposes an info action.
	ScenarioPropose = "info_proposal"
	// ScenarioGoDark: "go_dark" or "go_dark:<pool actor>" asks for a pool to
	// go dark, through the stake-floor guard. Without an actor it is the
	// lowest-numbered pool actor that is live.
	ScenarioGoDark = "go_dark"
	// ScenarioHotKeys: the network proposer pays to authorise a hot key for
	// every genesis committee member.
	ScenarioHotKeys = "hot_keys"
	// ScenarioSPOProbe: the richest registered pool votes on an open
	// proposal stake pools may not vote on. The ledger must refuse it
	// (DisallowedVoters); a node that accepts it is a fatal finding.
	ScenarioSPOProbe = "spo_vote_probe"
	// ScenarioUpgrade: "upgrade:<image>" or "upgrade:<epochs>:<image>"
	// starts an upgrade wave over the pools, adopting along a logistic
	// curve over that many epochs (one by default). An image reference
	// holds colons of its own, so the epochs come first.
	ScenarioUpgrade = "upgrade"
	// ScenarioHardFork: "hard_fork" or "hard_fork:<major.minor>" has the
	// network proposer propose a hard fork to that version, the
	// next major by default.
	ScenarioHardFork = "hard_fork"
)

// ScenarioInfo describes one scenario an inject control may name, for the
// UI's scenario list.
type ScenarioInfo struct {
	// Name is the scenario's name, the part of an inject before any colon.
	Name string `json:"name"`
	// Arg describes the argument after the colon; empty when the scenario
	// takes none.
	Arg string `json:"arg,omitempty"`
	// Optional reports that the argument may be left out.
	Optional bool `json:"optional,omitempty"`
	// Example is a valid argument, shown as the input's placeholder.
	Example string `json:"example,omitempty"`
	// Description says what injecting the scenario does.
	Description string `json:"description"`
}

// Catalog lists every scenario inject routes, in display order.
func Catalog() []ScenarioInfo {
	return []ScenarioInfo{
		{
			Name:        ScenarioPropose,
			Description: "The network proposer proposes an info action, a governance poll that changes nothing, so voters have something to vote on.",
		},
		{
			Name:        ScenarioGoDark,
			Arg:         "pool actor id",
			Optional:    true,
			Example:     "3",
			Description: "Takes a pool offline (its node stops). Without an actor id it is the lowest-numbered live pool. The stake-floor guard refuses it if too little stake would stay online.",
		},
		{
			Name:        ScenarioHotKeys,
			Description: "The network proposer pays to authorise a hot key for every genesis constitutional committee member, so the committee can vote.",
		},
		{
			Name:        ScenarioSPOProbe,
			Description: "The richest registered pool votes on an open proposal stake pools may not vote on. The ledger must refuse it (DisallowedVoters); a node that accepts it is a fatal finding.",
		},
		{
			Name:        ScenarioHardFork,
			Arg:         "protocol version major.minor",
			Optional:    true,
			Example:     "11.0",
			Description: "The network proposer proposes a hard fork to the version (the next major by default). The committee, the DReps and the pools vote; once ratified at an epoch boundary the network moves to the version at the next, and every node must follow. Refused while a hard fork is open or ratified and not yet enacted.",
		},
		{
			Name:        ScenarioUpgrade,
			Arg:         "[epochs:]image",
			Example:     "2:ghcr.io/blinklabs-io/dingo:main",
			Description: "Starts an upgrade wave: every registered pool switches to the image, adopting along an S-curve over that many epochs (one by default). Eager pools adopt first.",
		},
	}
}

// upgradeSteepness is a wave's logistic half-width in e-folds.
const upgradeSteepness = 4

// Scenarios routes an inject control to the intent that carries it out.
type Scenarios interface {
	// Propose has the network proposer propose an info action.
	Propose(ctx context.Context, at sched.Time) (string, error)
	// SPOProbe has the richest registered pool vote where pools may not.
	SPOProbe(ctx context.Context, at sched.Time) (string, error)
	// GoDark asks the stake floor to take a pool dark (arg names the pool,
	// empty for the first live one).
	GoDark(at sched.Time, arg string) string
	// HotKeys has the network proposer authorise every committee hot key.
	HotKeys(ctx context.Context, at sched.Time) (string, error)
	// HardFork has the network proposer propose a hard fork to
	// version arg ("major.minor"; empty for the next major).
	HardFork(ctx context.Context, at sched.Time, arg string) (string, error)
}

// Roster is the pool actors as an upgrade wave and the pool plan read
// them.
type Roster interface {
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

// Clock queues an adoption event.
type Clock interface {
	Adopt(at sched.Time, i uint64)
}

// Journal is the decision log of the input being handled.
type Journal interface {
	Emit(at sched.Time, kind string, a population.ActorID, body any)
}

// Spec is what a Steer is built from.
type Spec struct {
	Seed  rng.Seed
	Slots sched.Slots
}

// Env is what Steer shares with the rest of the engine.
type Env struct {
	Scenarios Scenarios
	Roster    Roster
	Clock     Clock
	Journal   Journal
}

// Steer owns the run's controls and upgrade waves.
type Steer struct {
	spec Spec
	env  Env

	// paused stops the agendas; rateK multiplies every actor's rate (zero
	// is 1); weights override the profiles' intent weights.
	paused  bool
	rateK   uint32
	weights map[profile.IntentKind]float64
	// waves counts upgrade waves; adoptions are their queued image
	// changes and images the plan they built, by pool actor.
	waves     uint64
	adoptions []wire.Adoption
	images    map[population.ActorID]string
}

func New(sp Spec, env Env) *Steer {
	return &Steer{spec: sp, env: env, images: map[population.ActorID]string{}}
}

// Paused reports that the agendas are stopped.
func (s *Steer) Paused() bool { return s.paused }

// RateK is the rate multiplier; less than one reads as one.
func (s *Steer) RateK() uint32 { return s.rateK }

// Weights are the intent weights the controls override, by kind; nil when
// no override is in force (none was set, or clear_weights dropped it).
func (s *Steer) Weights() map[profile.IntentKind]float64 { return s.weights }

// Control applies one control input and acknowledges it with a Controlled
// decision.
func (s *Steer) Control(ctx context.Context, at sched.Time, c wire.Control) error {
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
	case "clear_weights":
		s.weights = nil
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
func (s *Steer) inject(ctx context.Context, at sched.Time, sc string) (string, error) {
	name, arg, _ := strings.Cut(sc, ":")
	switch name {
	case ScenarioPropose:
		return s.env.Scenarios.Propose(ctx, at)
	case ScenarioGoDark:
		return s.env.Scenarios.GoDark(at, arg), nil
	case ScenarioHotKeys:
		return s.env.Scenarios.HotKeys(ctx, at)
	case ScenarioSPOProbe:
		return s.env.Scenarios.SPOProbe(ctx, at)
	case ScenarioUpgrade:
		return s.startWave(at, arg), nil
	case ScenarioHardFork:
		return s.env.Scenarios.HardFork(ctx, at, arg)
	}
	return "unknown scenario", nil
}

// startWave plans an upgrade wave from at: every pool actor still
// registered adopts image at a time internal/upgrade draws.
func (s *Steer) startWave(at sched.Time, arg string) string {
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
	for _, id := range s.env.Roster.PoolIDs() {
		if s.env.Roster.Retired(id) {
			continue
		}
		pools = append(pools, upgrade.Pool{ID: strconv.FormatUint(uint64(id), 10), Eagerness: s.env.Roster.Eager(id)})
	}
	s.waves++
	src := s.spec.Seed.Derive("upgrade", strconv.FormatUint(s.waves, 10)).Rand()
	for _, in := range upgrade.Plan(c, image, pools, src) {
		s.adoptions = append(s.adoptions, wire.Adoption{At: in.At, Pool: in.Pool, Ref: in.Ref})
		s.env.Clock.Adopt(in.At, uint64(len(s.adoptions)-1))
	}
	return ""
}

// Adopt records one pool's adoption: the image the plan now gives it.
func (s *Steer) Adopt(at sched.Time, i uint64) {
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

// PoolPlan is what the engine wants of one pool actor's ContinuumPool;
// see wire.PoolPlan.
type PoolPlan = wire.PoolPlan

// Pools is the pool plan, by actor. A live run patches each ContinuumPool
// from it; the fake has nothing to patch.
func (s *Steer) Pools() []PoolPlan {
	var out []PoolPlan
	for _, id := range s.env.Roster.PoolIDs() {
		p := PoolPlan{Actor: uint64(id), Genesis: -1, Dark: s.env.Roster.Dark(id), Image: s.images[id]}
		if g, ok := s.env.Roster.Genesis(id); ok {
			p.Genesis = g
		}
		out = append(out, p)
	}
	return out
}

// Saved is Steer's section of a snapshot. Weights are ascending by kind
// and Images by pool; no weights means no override.
type Saved struct {
	// Paused stops the agendas.
	Paused bool `json:"paused,omitzero"`
	// RateK multiplies every actor's rate; zero is none.
	RateK uint32 `json:"rate_k,omitzero"`
	// Waves counts the upgrade waves started.
	Waves uint64 `json:"waves,omitzero"`
	// Adoptions are the waves' queued image changes.
	Adoptions []wire.Adoption `json:"adoptions,omitempty"`
	// Weights are the operator's intent weight overrides.
	Weights []wire.WeightRec `json:"weights,omitempty"`
	// Images are the node images waves moved pool actors to.
	Images []wire.ImageRec `json:"images,omitempty"`
}

// Save returns the state a snapshot keeps.
func (s *Steer) Save() Saved {
	v := Saved{Paused: s.paused, RateK: s.rateK, Waves: s.waves, Adoptions: slices.Clone(s.adoptions)}
	for _, k := range slices.Sorted(maps.Keys(s.weights)) {
		v.Weights = append(v.Weights, wire.WeightRec{Kind: string(k), Weight: s.weights[k]})
	}
	for _, id := range slices.Sorted(maps.Keys(s.images)) {
		v.Images = append(v.Images, wire.ImageRec{Pool: uint64(id), Ref: s.images[id]})
	}
	return v
}

// Load restores a Save's state and queues the adoptions not yet done.
func (s *Steer) Load(v Saved) {
	s.paused, s.rateK, s.waves, s.adoptions = v.Paused, v.RateK, v.Waves, v.Adoptions
	if len(v.Weights) > 0 {
		s.weights = map[profile.IntentKind]float64{}
		for _, w := range v.Weights {
			s.weights[profile.IntentKind(w.Kind)] = w.Weight
		}
	}
	for _, r := range v.Images {
		s.images[population.ActorID(r.Pool)] = r.Ref
	}
	for i, a := range s.adoptions {
		if !a.Done {
			s.env.Clock.Adopt(a.At, uint64(i))
		}
	}
}
