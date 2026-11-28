// Package cohort is the engine's population: its actors and their wallets
// and pool keys, the lifecycle edges that move them, the birth streams that
// add them, the genesis pools some of them operate and the identity rows the
// UI reads. It schedules and emits only through the interfaces below, which
// the composite satisfies, so none of it reaches the other components.
//
//	Roster    the actors, their records and the address index
//	Life      spawn, the hazard clock and the chain-gated lifecycle edges
//	Births    the per-profile birth streams
//	Keyring   wallets, DRep and pool keys, and the genesis pools (Genesis)
//	Idents    the identity rows
//
// The composite orders every call and emit: a Cohort method never starts
// work on its own, so the order of RNG draws (each actor's Draws counter)
// and of journal records is the caller's.
package cohort

import (
	"fmt"

	"github.com/chrisguiney/cardano-continuum/internal/agenda"
	"github.com/chrisguiney/cardano-continuum/internal/chain"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/wire"
	"github.com/chrisguiney/cardano-continuum/internal/keys"
	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
	"github.com/chrisguiney/cardano-continuum/internal/rng"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

// Faucet is the faucet's owner ID in the ledger. It is above every census
// ID, so no actor can take it.
const Faucet = population.ActorID(keys.FaucetID)

// Script owns the always-succeeds address in the view's book, so the
// payments to it are recognized as the script's.
const Script = population.ActorID(keys.FaucetID - 1)

// Scheduler is the event queue as the cohort uses it.
type Scheduler interface {
	// Hazard schedules an actor's hazard event.
	Hazard(at sched.Time, id population.ActorID) sched.Handle
	// Act schedules an actor's next act.
	Act(at sched.Time, id population.ActorID) sched.Handle
	// Birth schedules a birth stream's next birth.
	Birth(at sched.Time, idx int) sched.Handle
	// Money schedules a human's next money step.
	Money(at sched.Time, h population.HumanID) sched.Handle
	// Quote schedules the market's next Quote record.
	Quote(at sched.Time)
	// Cancel removes a scheduled event.
	Cancel(h sched.Handle)
}

// Emitter writes a decision to the journal.
type Emitter interface {
	Emit(at sched.Time, kind string, a population.ActorID, body any)
}

// Adversaries is what the cohort asks of the adversaries.
type Adversaries interface {
	// Arm returns pol with the actor's attacks, when its profile has them.
	Arm(id population.ActorID, p profile.Profile, pol agenda.Policy) agenda.Policy
	// Adversary reports whether the actor is an adversary.
	Adversary(id population.ActorID) bool
	// Spent is what the adversary has spent on attacks.
	Spent(id population.ActorID) uint64
}

// Pacer is what the cohort asks of the pacing: an actor's intent clocks,
// and its next wake.
type Pacer interface {
	Clocks(a population.Actor, p profile.Profile) ([]wire.Clock, profile.IntentKind)
	Wake(a population.Actor, st *Actor, now sched.Time)
}

// Chain is the chain side the cohort books into and reads.
type Chain interface {
	Book() *chain.Book
	View() *chain.View
}

// Env is what the cohort's parts share of the rest of the engine.
type Env struct {
	Sched Scheduler
	Emit  Emitter
	Chain Chain
	Att   Adversaries
	Pace  Pacer
	// Flight is the unfinished intents, which hold the humans' fiat.
	Flight Flight
}

// Spec is what a cohort is built from.
type Spec struct {
	// Seed is the run seed: the birth streams derive from it.
	Seed rng.Seed
	// Census is the stream root the census spawns actors from.
	Census    rng.Seed
	Slots     sched.Slots
	Proposer  string
	ActorKeys *keys.Store
	// RootKeys is the run seed's own keystore: genesis pools, committee.
	RootKeys  *keys.Store
	Pools     []GenesisPool
	PoolStake uint64
	// Markets is the run's ADA market; nil means none.
	Markets *profile.Markets
	// Humans is the run's humans section; nil means each wallet is its own
	// owner.
	Humans *profile.Humans
}

// Cohort is the population and its parts.
type Cohort struct {
	*Roster
	Life   *Life
	Births *Births
	Idents *Idents
	// Market is the ADA market the wallets trade in.
	Market *Market
	// Desk is the wallets' positions and trades on it.
	Desk *Desk
	// People is the humans who own the wallets.
	People *People
	// Money is the humans' household money.
	Money *Money
}

// New returns an empty cohort.
func New(sp Spec, v Env) *Cohort {
	ring := newKeyring(sp.ActorKeys, sp.RootKeys, &Genesis{Pools: sp.Pools, stake: sp.PoolStake, chain: v.Chain})
	r := newRoster(sp.Census, ring, v)
	m := newMarket(sp, r)
	desk := &Desk{m: m}
	pp := newPeople(sp, r, m)
	m.people = pp
	mo := newMoney(pp, v.Flight)
	desk.money = mo
	l := &Life{r: r, slots: sp.Slots, market: m, people: pp, money: mo}
	return &Cohort{
		People: pp,
		Money:  mo,
		Roster: r,
		Life:   l,
		Births: &Births{life: l, seed: sp.Seed, sched: v.Sched, emit: v.Emit},
		Idents: &Idents{r: r, proposer: sp.Proposer, att: v.Att, chain: v.Chain, desk: desk},
		Market: m,
		Desk:   desk,
	}
}

// Profiles looks a profile up by name.
type Profiles interface {
	Get(name string) (profile.Profile, bool)
}

// Save returns the snapshot's sections the cohort owns: every actor,
// ascending, and the birth streams in the order they were added.
func (c *Cohort) Save() ([]wire.ActorRec, []wire.BirthRec) {
	var actors []wire.ActorRec
	for _, id := range c.IDs() {
		a, _ := c.Get(id)
		st := c.Rec(id)
		paid := make([]uint64, len(st.Paid))
		for i, p := range st.Paid {
			paid[i] = uint64(p)
		}
		var (
			currency, venue string
			basis           float64
			units           uint64
		)
		if tr := st.Trader; tr != nil {
			currency, venue, basis, units = c.Market.Code(tr.Primary), venueName(c.Market.path.Params(), tr.Venue), tr.Basis, tr.Units
		}
		actors = append(actors, wire.ActorRec{
			ID: uint64(id), Profile: a.Profile, State: string(a.State), Born: a.Born, Changed: a.Changed,
			Frustration: a.Frustration, Competence: a.Competence, HazardAt: st.HazardAt, HazardTo: string(st.HazardTo), ActAt: st.ActAt, Draws: st.Draws, Booting: st.Booting,
			Owed: st.Owed, Paid: paid, FollowUp: st.FollowUp, Waits: st.Waits, Currency: currency,
			Basis: basis, Units: units, Venue: venue,
		})
	}
	var births []wire.BirthRec
	for i := range c.Births.Len() {
		name, at, n := c.Births.Stream(i)
		births = append(births, wire.BirthRec{Profile: name, At: at, N: n, Currency: c.Births.Currency(i)})
	}
	return actors, births
}

// Load rebuilds the actors and birth streams a Save recorded. Each actor is
// respawned from the census seed, which reproduces its traits, walked to its
// recorded state and given its pending hazard and act again. The birth
// streams must be the ones the cohort was built with, in the same order.
func (c *Cohort) Load(ps Profiles, actors []wire.ActorRec, births []wire.BirthRec) error {
	for _, r := range actors {
		if err := c.revive(ps, r); err != nil {
			return err
		}
	}
	if len(births) != c.Births.Len() {
		return fmt.Errorf("engine: snapshot has %d birth streams, config %d", len(births), c.Births.Len())
	}
	for i, r := range births {
		if name, _, _ := c.Births.Stream(i); r.Profile != name || r.Currency != c.Births.Currency(i) {
			return fmt.Errorf("engine: snapshot birth stream %q %q, config %q %q", r.Profile, r.Currency, name, c.Births.Currency(i))
		}
		c.Births.Resume(i, r.At, r.N)
	}
	c.Market.Bind()
	c.Money.Resume()
	return nil
}

func (c *Cohort) revive(ps Profiles, r wire.ActorRec) error {
	p, ok := ps.Get(r.Profile)
	if !ok {
		return fmt.Errorf("engine: snapshot actor %d has unknown profile %q", r.ID, r.Profile)
	}
	a := c.Revive(p, r.Born, c.People.owner(population.ActorID(r.ID)), c.People.On())
	if uint64(a.ID) != r.ID {
		return fmt.Errorf("engine: snapshot actor %d out of order (got %d)", r.ID, a.ID)
	}
	if err := c.Life.Walk(a, population.State(r.State), r.Changed); err != nil {
		return err
	}
	if err := c.Pop().SetFrustration(a.ID, r.Frustration); err != nil {
		return err
	}
	if err := c.Pop().SetCompetence(a.ID, r.Competence); err != nil {
		return err
	}
	st := c.Register(a, p)
	c.Market.Join(a, st, currencyOf(c.Market, r.Currency))
	c.Desk.restore(st, r)
	st.HazardAt, st.HazardTo, st.ActAt, st.Draws = r.HazardAt, population.State(r.HazardTo), r.ActAt, r.Draws
	st.Booting = r.Booting
	st.Owed = r.Owed
	st.FollowUp, st.Waits = r.FollowUp, r.Waits
	for _, id := range r.Paid {
		st.Paid = append(st.Paid, population.ActorID(id))
	}
	if st.HazardAt != population.Never {
		st.HazardH = c.env.Sched.Hazard(st.HazardAt, a.ID)
	}
	if st.ActAt != population.Never {
		st.ActH = c.env.Sched.Act(st.ActAt, a.ID)
	}
	return nil
}

// Holdings is how many wallets each human ever owned, by human ID; nil
// without a humans section.
func (c *Cohort) Holdings() []int {
	pp := c.People
	if !pp.On() {
		return nil
	}

	out := make([]int, len(pp.list))
	for i, h := range pp.list {
		out[i] = len(walletsOf(c.Roster, h.ID))
	}
	return out
}
