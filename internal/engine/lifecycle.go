package engine

import (
	"encoding/hex"
	"fmt"

	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

// This file moves pools and DReps along their chain-gated lifecycle edges:
// on the settlement of the certificate that causes them (population.
// Confirmed), and on the epoch transition that applies a retirement
// (population.EpochApplied). Settled means K deep, as everything durable.

// settle accounts for a settled attempt of intent p: the deposit it paid
// or refunded, a locked output it freed, and the lifecycle edge it causes.
func (e *Engine) settle(at sched.Time, p *pendingRec, tx TxID) error {
	switch d := specs[p.kindOf()].deposit(e, p); {
	case d >= 0:
		e.deposits += uint64(d)
	case uint64(-d) <= e.deposits:
		e.deposits -= uint64(-d)
	default:
		// A refund of a deposit paid before the engine's genesis (a
		// genesis registration) comes from outside the population.
		e.foreign -= min(e.foreign, uint64(-d)-e.deposits)
		e.deposits = 0
	}
	if p.Kind == unlockKind {
		// The lock's script output counted as foreign when it settled.
		e.foreign -= min(e.foreign, p.Amount)
	}
	id := population.ActorID(p.Actor)
	a, ok := e.census.Get(id)
	if !ok {
		return nil
	}
	cause := population.Confirmed{Tx: hex.EncodeToString(tx[:])}
	switch profile.IntentKind(p.Kind) {
	case profile.IntentRegisterPool:
		return e.move(at, a, population.Active, cause)
	case profile.IntentRetirePool:
		return e.move(at, a, population.Retiring, cause)
	case profile.IntentRegisterDRep:
		return e.move(at, a, population.Voting, cause)
	case profile.IntentUpdatePool:
		// A re-registration cancels a pending retirement.
		return e.move(at, a, population.Active, cause)
	case profile.IntentRetireDRep:
		if err := e.move(at, a, population.Expired, cause); err != nil {
			return err
		}
		if st := e.actors[id]; st.actAt != population.Never {
			e.queue.Cancel(st.actH)
			st.actAt = population.Never
		}
	}
	return nil
}

// move applies a lifecycle edge if the actor's kind has it from its
// current state; any other request is a no-op, since a confirmation can
// arrive for an actor that has already moved on.
func (e *Engine) move(at sched.Time, a population.Actor, to population.State, cause population.Cause) error {
	if !population.Allowed(a.Kind, a.State, to) {
		return nil
	}
	if err := e.census.Apply(population.Transition{ID: a.ID, To: to, At: at, Cause: cause}); err != nil {
		return fmt.Errorf("engine: actor %d: %w", a.ID, err)
	}
	e.emit(at, KindTransition, a.ID, Transition{From: string(a.State), To: string(to)})
	return nil
}

// epochApplied retires every retiring pool actor whose retirement the
// confirmed view's epoch transition into epoch applied.
func (e *Engine) epochApplied(at sched.Time, epoch uint64) error {
	c := e.view.Confirmed()
	for _, id := range e.census.IDs(profile.Pool, population.Retiring) {
		if c.PoolRegistered(id) {
			continue
		}
		a, _ := e.census.Get(id)
		if err := e.move(at, a, population.Retired, population.EpochApplied{Epoch: epoch}); err != nil {
			return err
		}
		st := e.actors[id]
		if st.actAt != population.Never {
			e.queue.Cancel(st.actH)
			st.actAt = population.Never
		}
	}
	return nil
}

// lifePath is each kind's lifecycle in order from its initial state, so
// restore can walk an actor to its recorded state.
var lifePath = map[profile.Kind][]population.State{
	profile.Wallet: {population.Born, population.Active},
	profile.Pool:   {population.Registered, population.Active, population.Retiring, population.Retired},
	profile.DRep:   {population.Registered, population.Voting, population.Expired},
}

// walk moves a freshly spawned actor to state to at time at. Chain-gated
// edges take a cause standing for the confirmation the snapshot recorded.
func (e *Engine) walk(a population.Actor, to population.State, at sched.Time) error {
	path := lifePath[a.Kind]
	if a.Kind == profile.Wallet && to != population.Born && to != population.Active {
		path = append(path, to)
	}
	from := a.State
	for _, s := range path[1:] {
		if from == to {
			return nil
		}
		var cause population.Cause = population.Timer{}
		switch {
		case s == population.Retired:
			cause = population.EpochApplied{Epoch: e.cfg.Slots.EpochAt(at)}
		case population.ChainGated(a.Kind, from, s):
			cause = population.Confirmed{Tx: "snapshot"}
		}
		if err := e.census.Apply(population.Transition{ID: a.ID, To: s, At: at, Cause: cause}); err != nil {
			return fmt.Errorf("engine: restore actor %d: %w", a.ID, err)
		}
		from = s
	}
	if from != to {
		return fmt.Errorf("engine: restore actor %d: state %s unreachable", a.ID, to)
	}
	return nil
}
