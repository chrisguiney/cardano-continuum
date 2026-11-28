package population

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"strconv"

	"github.com/chrisguiney/cardano-continuum/internal/check"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
	"github.com/chrisguiney/cardano-continuum/internal/rng"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

// ActorID identifies an actor. IDs are dense, start at 1 and are assigned in
// spawn order.
type ActorID uint64

// Sampled holds an actor's drawn personal parameters.
type Sampled struct {
	profile.SampledTraits
	// RateMult multiplies the profile's base activity rates. It is Pareto
	// distributed with scale 1 and the profile's tail index
	// (profile.Rates.TailIndex), so a few actors are far busier than the rest.
	RateMult float64
}

// Actor is one member of the population. It is a comparable value; Census
// returns copies.
type Actor struct {
	ID      ActorID
	Kind    profile.Kind
	Profile string
	State   State
	Traits  Sampled
	// Born is the spawn time and Changed the time of the last state change.
	Born, Changed sched.Time
	// Frustration is non-negative and raises the dormancy hazard.
	Frustration float64
	Competence  float64
	// Seed roots the actor's private random streams. It derives from the
	// census root seed and the actor's ID alone.
	Seed rng.Seed
	// Owner is the human that owns the wallet; zero for the actors no person
	// owns (pools, DReps, contracts, the faucet).
	Owner HumanID
	// Purpose is what the wallet is for to its owner; empty without an
	// owner.
	Purpose Purpose
}

// Stream returns a fresh generator on the actor's stream named by path. A
// caller that needs a continuing sequence keeps the result.
func (a Actor) Stream(path ...string) *rand.Rand { return a.Seed.Derive(path...).Rand() }

type bucket struct {
	kind  profile.Kind
	state State
}

// Census holds every actor and indexes them by kind and state. The zero value
// is an empty census. It is not safe for concurrent use.
type Census struct {
	actors []Actor // actors[id-1]
	index  map[bucket]map[ActorID]struct{}
	// humans are the owners of the wallets by ID.
	humans map[HumanID]*Human
}

// Len returns the number of actors ever spawned.
func (c *Census) Len() int { return len(c.actors) }

// Spawn creates an actor from p at time now. root is the census root seed:
// the actor's streams derive from root and its ID only, so spawning another
// actor never changes an existing one. Traits and the activity multiplier
// draw from separate streams so adding a draw to one leaves the other fixed.
func (c *Census) Spawn(p profile.Profile, root rng.Seed, now sched.Time) Actor {
	var o Owner
	if p.Kind == profile.Wallet {
		o = Owner{Human: HumanID(len(c.actors) + 1), Purpose: General}
	}
	return c.SpawnOwned(p, root, now, o)
}

// SpawnOwned spawns an actor as Spawn does, owned by o.Human: the wallet
// founds that human, which takes the wallet's traits as its own, when no
// human has the ID yet, and joins it otherwise. A zero o.Human spawns an
// actor no person owns.
func (c *Census) SpawnOwned(p profile.Profile, root rng.Seed, now sched.Time, o Owner) Actor {
	id := ActorID(len(c.actors) + 1)
	seed := root.Derive("actor", strconv.FormatUint(uint64(id), 10))
	traits := p.Traits.Sample(seed.Derive("traits").Rand())
	rate := profile.Pareto{Xm: 1, Alpha: p.Rates.TailIndex()}.Sample(seed.Derive("rate").Rand())
	a := Actor{
		ID: id, Kind: p.Kind, Profile: p.Name, State: initialState[p.Kind],
		Traits: Sampled{SampledTraits: traits, RateMult: rate},
		Born:   now, Changed: now, Competence: traits.Competence, Seed: seed,
	}
	c.own(&a, o, p.Agenda)
	c.actors = append(c.actors, a)
	c.add(a)
	return c.owned(a)
}

// Get returns the actor with the given ID. A wallet carries its owner's
// traits and competence.
func (c *Census) Get(id ActorID) (Actor, bool) {
	if id == 0 || uint64(id) > uint64(len(c.actors)) {
		return Actor{}, false
	}
	return c.owned(c.actors[id-1]), true
}

// Apply performs a transition after checking it against the lifecycle table
// and, for chain effects, its cause. A refused transition changes nothing.
func (c *Census) Apply(tr Transition) error {
	if tr.ID == 0 || uint64(tr.ID) > uint64(len(c.actors)) {
		return fmt.Errorf("%w: %d", ErrUnknownActor, tr.ID)
	}

	a := c.actors[tr.ID-1]
	if err := tr.check(a.Kind, a.State); err != nil {
		return err
	}
	c.remove(a)
	a.State, a.Changed = tr.To, tr.At
	c.actors[a.ID-1] = a
	c.add(a)
	check.PostFn(c.consistent, "population: indexes agree with a scan")
	return nil
}

// SetFrustration sets an actor's frustration, clamping it at zero.
func (c *Census) SetFrustration(id ActorID, f float64) error {
	if _, ok := c.Get(id); !ok {
		return fmt.Errorf("%w: %d", ErrUnknownActor, id)
	}
	c.actors[id-1].Frustration = max(f, 0)
	return nil
}

// SetCompetence sets an actor's competence, clamped to [0, 1]: what
// learning from its own fixes has made of its drawn trait. A wallet's
// competence is its owner's, so the owner learns for all its wallets.
func (c *Census) SetCompetence(id ActorID, v float64) error {
	a, ok := c.Get(id)
	if !ok {
		return fmt.Errorf("%w: %d", ErrUnknownActor, id)
	}
	v = min(max(v, 0), 1)
	if h, ok := c.humans[a.Owner]; ok {
		h.Competence = v
	}
	c.actors[id-1].Competence = v
	return nil
}

// Count returns how many actors of kind are in state st.
func (c *Census) Count(kind profile.Kind, st State) int {
	return len(c.index[bucket{kind, st}])
}

// IDs returns the IDs of actors of kind in state st in ascending order.
func (c *Census) IDs(kind profile.Kind, st State) []ActorID {
	set := c.index[bucket{kind, st}]
	ids := make([]ActorID, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

func (c *Census) add(a Actor) {
	if c.index == nil {
		c.index = map[bucket]map[ActorID]struct{}{}
	}
	b := bucket{a.Kind, a.State}
	if c.index[b] == nil {
		c.index[b] = map[ActorID]struct{}{}
	}
	c.index[b][a.ID] = struct{}{}
}

func (c *Census) remove(a Actor) {
	delete(c.index[bucket{a.Kind, a.State}], a.ID)
}

func (c *Census) consistent() bool {
	scan := map[bucket]int{}
	for _, a := range c.actors {
		scan[bucket{a.Kind, a.State}]++
		if _, ok := c.index[bucket{a.Kind, a.State}][a.ID]; !ok {
			return false
		}
	}
	for b, set := range c.index {
		if len(set) != scan[b] {
			return false
		}
	}
	return true
}

// Human returns the human with the given ID.
func (c *Census) Human(id HumanID) (Human, bool) {
	h, ok := c.humans[id]
	if !ok {
		return Human{}, false
	}
	out := *h
	out.Wallets = slices.Clone(h.Wallets)
	return out, true
}

// Humans returns every human's ID in ascending order.
func (c *Census) Humans() []HumanID {
	ids := make([]HumanID, 0, len(c.humans))
	for id := range c.humans {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

// own gives a to the human o names: a new one, which takes the wallet's
// traits as its own, or an existing one. A zero o.Human leaves a ownerless.
func (c *Census) own(a *Actor, o Owner, motivation profile.AgendaName) {
	if o.Human == 0 {
		return
	}

	a.Owner, a.Purpose = o.Human, o.Purpose
	if h, ok := c.humans[o.Human]; ok {
		h.Wallets = append(h.Wallets, a.ID)
		return
	}

	if c.humans == nil {
		c.humans = map[HumanID]*Human{}
	}
	c.humans[o.Human] = &Human{
		ID:         o.Human,
		Motivation: motivation,
		Traits:     a.Traits.SampledTraits,
		Competence: a.Competence,
		Born:       a.Born,
		Wallets:    []ActorID{a.ID},
	}
}

// owned is a with its owner's traits and competence.
func (c *Census) owned(a Actor) Actor {
	h, ok := c.humans[a.Owner]
	if !ok {
		return a
	}
	a.Traits.SampledTraits, a.Competence = h.Traits, h.Competence
	return a
}
