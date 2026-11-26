// Package population tracks who exists and in which lifecycle state, and when
// each actor's own clock next moves it. It decides birth, activity level,
// dormancy and death only; choosing what an actor does is the agenda's job.
//
// The package is pure: it reads no wall clock, starts no goroutine and draws
// randomness only from streams the caller passes in or that derive from an
// actor's own seed.
package population

import (
	"errors"
	"fmt"
	"slices"

	"github.com/chrisguiney/cardano-continuum/internal/profile"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

// State is a lifecycle state. Names are shared across kinds where the
// meaning is the same (Active, Abandoned); Allowed says which apply to which.
type State string

// Lifecycle states.
const (
	Born       State = "born"
	Active     State = "active"
	Dormant    State = "dormant"
	Abandoned  State = "abandoned"
	Registered State = "registered"
	Retiring   State = "retiring"
	Retired    State = "retired"
	Voting     State = "voting"
	Expired    State = "expired"
	Deployed   State = "deployed"
	Used       State = "used"
)

var allStates = []State{Born, Active, Dormant, Abandoned, Registered, Retiring, Retired, Voting, Expired, Deployed, Used}

// Errors returned by Apply. Callers match them with errors.Is.
var (
	ErrIllegalTransition = errors.New("illegal lifecycle transition")
	ErrNotConfirmed      = errors.New("transition requires chain confirmation")
	ErrUnknownActor      = errors.New("unknown actor")
)

type edge struct {
	kind     profile.Kind
	from, to State
}

// gate is the cause an edge needs. A chain effect is applied only from the
// chain: a transaction's confirmation, or an epoch transition the ledger has
// run.
type gate int

const (
	// free edges follow the actor's own clock or the engine's decision.
	free gate = iota
	// confirmed edges need the confirmation of a transaction.
	confirmed
	// epochApplied edges need an epoch transition that applied the effect.
	epochApplied
)

// lifecycle lists every legal edge and the cause it needs. Faucets never
// leave Active, so they have no edges.
//
// A pool is Retiring once its retirement certificate confirms, Retired only
// when the epoch transition the certificate names has retired it, and back
// to Active when a re-registration confirms before then (the stake-floor
// guard contract, rule 11, in models/specs/stake-floor). Marking it Retired
// on the certificate would let a reader drop its stake from the leader
// schedule two epochs early.
var lifecycle = map[edge]gate{
	{profile.Wallet, Born, Active}:       free,
	{profile.Wallet, Active, Dormant}:    free,
	{profile.Wallet, Dormant, Active}:    free,
	{profile.Wallet, Active, Abandoned}:  free,
	{profile.Wallet, Dormant, Abandoned}: free,

	{profile.Pool, Registered, Active}: confirmed,
	{profile.Pool, Active, Retiring}:   confirmed,
	{profile.Pool, Retiring, Active}:   confirmed,
	{profile.Pool, Retiring, Retired}:  epochApplied,

	{profile.DRep, Registered, Voting}: free,
	{profile.DRep, Voting, Expired}:    confirmed,

	{profile.Contract, Deployed, Used}:  free,
	{profile.Contract, Used, Abandoned}: free,
}

// initialState is the state Spawn gives a new actor of each kind.
var initialState = map[profile.Kind]State{
	profile.Wallet:   Born,
	profile.Pool:     Registered,
	profile.DRep:     Registered,
	profile.Contract: Deployed,
	profile.Faucet:   Active,
}

// Allowed reports whether kind may move from one state to another.
func Allowed(kind profile.Kind, from, to State) bool {
	_, ok := lifecycle[edge{kind, from, to}]
	return ok
}

// ChainGated reports whether the edge reflects a chain effect (a pool going
// active, retiring or retired, a DRep expiring) and so is refused unless its
// cause comes from the chain: a Confirmed, or an EpochApplied for a pool's
// retirement.
func ChainGated(kind profile.Kind, from, to State) bool {
	return lifecycle[edge{kind, from, to}] != free
}

// Cause says why a transition happens.
type Cause interface{ cause() }

// Timer is a transition fired by the actor's own hazard clock.
type Timer struct{}

// Intent is a transition the engine or an agenda chose, before the chain
// shows any effect of it. It moves no chain-gated edge.
type Intent struct{}

// Confirmed is a transition caused by a transaction seen on the confirmed
// chain. Tx names the confirming transaction and must be set.
type Confirmed struct{ Tx string }

// EpochApplied is a transition caused by an epoch transition the ledger has
// run, such as a pool's retirement taking effect. Epoch is the epoch the
// transition opened.
type EpochApplied struct{ Epoch uint64 }

func (Timer) cause()        {}
func (Intent) cause()       {}
func (Confirmed) cause()    {}
func (EpochApplied) cause() {}

// Transition requests moving actor ID to state To at time At.
type Transition struct {
	ID    ActorID
	To    State
	At    sched.Time
	Cause Cause
}

func (t Transition) check(kind profile.Kind, from State) error {
	if !Allowed(kind, from, t.To) {
		return fmt.Errorf("%w: %s %s->%s", ErrIllegalTransition, kind, from, t.To)
	}
	if !satisfies(lifecycle[edge{kind, from, t.To}], t.Cause) {
		return fmt.Errorf("%w: %s %s->%s by %T", ErrNotConfirmed, kind, from, t.To, t.Cause)
	}
	return nil
}

func satisfies(g gate, c Cause) bool {
	switch g {
	case confirmed:
		tx, ok := c.(Confirmed)
		return ok && tx.Tx != ""
	case epochApplied:
		_, ok := c.(EpochApplied)
		return ok
	}
	return true
}

// States returns every lifecycle state.
func States() []State { return slices.Clone(allStates) }
