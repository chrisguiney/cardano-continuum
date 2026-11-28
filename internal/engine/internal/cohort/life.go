package cohort

import (
	"encoding/hex"
	"fmt"

	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/wire"
	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

// Life moves actors along their lifecycle: it spawns them, runs their
// hazard clocks, and applies the chain-gated edges a confirmed certificate
// or an epoch transition causes. Settled means K deep, as everything
// durable.
type Life struct {
	r     *Roster
	slots sched.Slots
	// market gives each new wallet its currencies.
	market *Market
}

// Spawn adds an actor and schedules its first hazard. A wallet's primary
// currency is currency, or drawn by share when currency is negative.
func (l *Life) Spawn(p profile.Profile, at sched.Time, currency int) population.Actor {
	a := l.r.census.Spawn(p, l.r.seed, at)
	st := l.r.Register(a, p)
	l.market.Join(a, st, currency)
	l.NextHazard(a, st, at)
	// Pools and DReps have no hazard clock to wake them: they act from
	// birth, registering first.
	if p.Kind != profile.Wallet {
		l.r.env.Pace.Wake(a, st, at)
	}
	return a
}

// NextHazard draws the actor's next lifecycle hazard and schedules it.
func (l *Life) NextHazard(a population.Actor, st *Actor, now sched.Time) {
	st.HazardAt, st.HazardTo = population.NextHazard(a, st.Prof, st.Draw(a, "hazard"), now)
	if st.HazardAt != population.Never {
		st.HazardH = l.r.env.Sched.Hazard(st.HazardAt, a.ID)
	}
}

// Hazard applies the actor's due hazard: its state moves, its next wake and
// next hazard are drawn.
func (l *Life) Hazard(at sched.Time, id population.ActorID) error {
	a, _ := l.r.census.Get(id)
	st := l.r.actors[id]
	from, to := a.State, st.HazardTo
	st.HazardAt = population.Never
	if err := l.r.census.Apply(population.Transition{ID: id, To: to, At: at, Cause: population.Timer{}}); err != nil {
		return fmt.Errorf("engine: actor %d: %w", id, err)
	}
	l.r.env.Emit.Emit(at, wire.KindTransition, id, wire.Transition{From: string(from), To: string(to)})
	a, _ = l.r.census.Get(id)
	switch {
	case to == population.Active:
		l.r.env.Pace.Wake(a, st, at)
	case from == population.Active && st.ActAt != population.Never:
		l.r.env.Sched.Cancel(st.ActH)
		st.ActAt = population.Never
	}
	l.NextHazard(a, st, at)
	return nil
}

// Settled applies the lifecycle edge a settled attempt of an intent of the
// given kind causes for its actor.
func (l *Life) Settled(at sched.Time, id population.ActorID, kind profile.IntentKind, tx wire.TxID) error {
	a, ok := l.r.census.Get(id)
	if !ok {
		return nil
	}
	cause := population.Confirmed{Tx: hex.EncodeToString(tx[:])}
	switch kind {
	case profile.IntentRegisterPool:
		return l.move(at, a, population.Active, cause)
	case profile.IntentRetirePool:
		return l.move(at, a, population.Retiring, cause)
	case profile.IntentRegisterDRep:
		return l.move(at, a, population.Voting, cause)
	case profile.IntentUpdatePool:
		// A re-registration cancels a pending retirement.
		return l.move(at, a, population.Active, cause)
	case profile.IntentRetireDRep:
		if err := l.move(at, a, population.Expired, cause); err != nil {
			return err
		}
		if st := l.r.actors[id]; st.ActAt != population.Never {
			l.r.env.Sched.Cancel(st.ActH)
			st.ActAt = population.Never
		}
	}
	return nil
}

// move applies a lifecycle edge if the actor's kind has it from its
// current state; any other request is a no-op, since a confirmation can
// arrive for an actor that has already moved on.
func (l *Life) move(at sched.Time, a population.Actor, to population.State, cause population.Cause) error {
	if !population.Allowed(a.Kind, a.State, to) {
		return nil
	}
	if err := l.r.census.Apply(population.Transition{ID: a.ID, To: to, At: at, Cause: cause}); err != nil {
		return fmt.Errorf("engine: actor %d: %w", a.ID, err)
	}
	l.r.env.Emit.Emit(at, wire.KindTransition, a.ID, wire.Transition{From: string(a.State), To: string(to)})
	return nil
}

// EpochApplied retires every retiring pool actor whose retirement the
// confirmed view's epoch transition into epoch applied.
func (l *Life) EpochApplied(at sched.Time, epoch uint64) error {
	c := l.r.env.Chain.View().Confirmed()
	for _, id := range l.r.census.IDs(profile.Pool, population.Retiring) {
		if c.PoolRegistered(id) {
			continue
		}
		a, _ := l.r.census.Get(id)
		if err := l.move(at, a, population.Retired, population.EpochApplied{Epoch: epoch}); err != nil {
			return err
		}
		st := l.r.actors[id]
		if st.ActAt != population.Never {
			l.r.env.Sched.Cancel(st.ActH)
			st.ActAt = population.Never
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

// Walk moves a freshly spawned actor to state to at time at. Chain-gated
// edges take a cause standing for the confirmation the snapshot recorded.
func (l *Life) Walk(a population.Actor, to population.State, at sched.Time) error {
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
			cause = population.EpochApplied{Epoch: l.slots.EpochAt(at)}
		case population.ChainGated(a.Kind, from, s):
			cause = population.Confirmed{Tx: "snapshot"}
		}
		if err := l.r.census.Apply(population.Transition{ID: a.ID, To: s, At: at, Cause: cause}); err != nil {
			return fmt.Errorf("engine: restore actor %d: %w", a.ID, err)
		}
		from = s
	}
	if from != to {
		return fmt.Errorf("engine: restore actor %d: state %s unreachable", a.ID, to)
	}
	return nil
}
