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

// lifecycle lists every legal edge; the value says whether it reflects a chain
// effect and so may be applied only from a confirmation. Faucets never leave
// Active, so they have no edges.
var lifecycle = map[edge]bool{
	{profile.Wallet, Born, Active}:       false,
	{profile.Wallet, Active, Dormant}:    false,
	{profile.Wallet, Dormant, Active}:    false,
	{profile.Wallet, Active, Abandoned}:  false,
	{profile.Wallet, Dormant, Abandoned}: false,

	{profile.Pool, Registered, Active}: true,
	{profile.Pool, Active, Retiring}:   false,
	{profile.Pool, Retiring, Retired}:  true,

	{profile.DRep, Registered, Voting}: false,
	{profile.DRep, Voting, Expired}:    true,

	{profile.Contract, Deployed, Used}:  false,
	{profile.Contract, Used, Abandoned}: false,
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
// active or retiring at an epoch boundary, a DRep expiring) and so is refused
// unless its cause is a Confirmed.
func ChainGated(kind profile.Kind, from, to State) bool {
	return lifecycle[edge{kind, from, to}]
}

// Cause says why a transition happens.
type Cause interface{ cause() }

// Timer is a transition fired by the actor's own hazard clock.
type Timer struct{}

// Confirmed is a transition caused by a transaction or epoch effect seen on
// the confirmed chain. Tx names the confirming transaction and must be set.
type Confirmed struct{ Tx string }

func (Timer) cause()     {}
func (Confirmed) cause() {}

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
	if ChainGated(kind, from, t.To) {
		if c, ok := t.Cause.(Confirmed); !ok || c.Tx == "" {
			return fmt.Errorf("%w: %s %s->%s", ErrNotConfirmed, kind, from, t.To)
		}
	}
	return nil
}
