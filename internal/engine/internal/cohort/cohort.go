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
	"github.com/chrisguiney/cardano-continuum/internal/agenda"
	"github.com/chrisguiney/cardano-continuum/internal/chain"
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
	// Birth schedules a birth stream's next birth.
	Birth(at sched.Time, idx int) sched.Handle
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
	Clocks(a population.Actor, p profile.Profile) ([]Clock, profile.IntentKind)
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
}

// Cohort is the population and its parts.
type Cohort struct {
	*Roster
	Life   *Life
	Births *Births
	Idents *Idents
}

// New returns an empty cohort.
func New(sp Spec, v Env) *Cohort {
	ring := newKeyring(sp.ActorKeys, sp.RootKeys, &Genesis{Pools: sp.Pools, stake: sp.PoolStake, chain: v.Chain})
	r := newRoster(sp.Census, ring, v)
	l := &Life{r: r, slots: sp.Slots}
	return &Cohort{Roster: r, Life: l,
		Births: &Births{life: l, seed: sp.Seed, sched: v.Sched, emit: v.Emit},
		Idents: &Idents{r: r, proposer: sp.Proposer, att: v.Att, chain: v.Chain}}
}
