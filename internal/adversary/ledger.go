package adversary

import (
	"cmp"
	"fmt"
	"maps"
	"slices"

	"github.com/chrisguiney/cardano-continuum/internal/population"
)

// Ledger is every adversary's cost account: what it spent in each epoch
// against its profile's cost_ceiling. It is not safe for concurrent use;
// the engine's loop owns it.
type Ledger struct {
	ceiling map[population.ActorID]uint64
	spent   map[account]uint64
}

type account struct {
	actor population.ActorID
	epoch uint64
}

// Charge is one actor's spend in one epoch, as the engine snapshots it.
type Charge struct {
	Actor uint64 `cbor:"1,keyasint"`
	Epoch uint64 `cbor:"2,keyasint"`
	Spent uint64 `cbor:"3,keyasint"`
}

// NewLedger returns an empty ledger.
func NewLedger() *Ledger {
	return &Ledger{ceiling: map[population.ActorID]uint64{}, spent: map[account]uint64{}}
}

// Limit sets the actor's per-epoch ceiling. An actor never limited has a
// ceiling of zero and can spend nothing.
func (l *Ledger) Limit(a population.ActorID, ceiling uint64) { l.ceiling[a] = ceiling }

// Charge records cost against the actor's epoch, or refuses it whole when
// it would take the epoch past the ceiling.
func (l *Ledger) Charge(a population.ActorID, epoch, cost uint64) error {
	left := l.Remaining(a, epoch)
	if cost > left {
		return fmt.Errorf("%w: actor %d epoch %d: cost %d, %d left of %d", ErrCeiling, a, epoch, cost, left, l.ceiling[a])
	}
	l.spent[account{a, epoch}] += cost
	return nil
}

// Remaining is what the actor may still spend in the epoch.
func (l *Ledger) Remaining(a population.ActorID, epoch uint64) uint64 {
	c, s := l.ceiling[a], l.spent[account{a, epoch}]
	if s >= c {
		return 0
	}
	return c - s
}

// Entries lists every nonzero charge, by actor then epoch.
func (l *Ledger) Entries() []Charge {
	out := make([]Charge, 0, len(l.spent))
	for _, k := range slices.SortedFunc(maps.Keys(l.spent), func(x, y account) int {
		return cmp.Or(cmp.Compare(x.actor, y.actor), cmp.Compare(x.epoch, y.epoch))
	}) {
		out = append(out, Charge{Actor: uint64(k.actor), Epoch: k.epoch, Spent: l.spent[k]})
	}
	return out
}

// Restore replaces the charges with cs; ceilings are left as set.
func (l *Ledger) Restore(cs []Charge) {
	l.spent = map[account]uint64{}
	for _, c := range cs {
		l.spent[account{population.ActorID(c.Actor), c.Epoch}] = c.Spent
	}
}
