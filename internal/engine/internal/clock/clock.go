// Package clock is the engine's event queue and notion of time. Events due at
// the same time come out in a fixed order, so a queue rebuilt from a snapshot
// runs them as the live one did.
package clock

import (
	"cmp"
	"errors"
	"fmt"
	"slices"

	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

// Kind is an event's kind. Same-time events run in Kind order.
type Kind uint8

// Event kinds in the order same-time events run.
const (
	Birth Kind = iota + 1
	Hazard
	Act
	// Retry only wakes the loop at the end of a backoff; InFlight reads
	// the payment's NotBefore against the clock.
	Retry
	// Refill is the faucet's round.
	Refill
	// Adopt is one pool's adoption in an upgrade wave; Intent indexes
	// the adoptions.
	Adopt
	// Deploy opens the next contract.
	Deploy
	// Fork is the run's own hard-fork step, at an epoch boundary.
	Fork
	// Quote records the market. It runs last at its time, so a record
	// reads the market every other same-time event saw.
	Quote
)

// Event is one scheduled event. Same-time events run sorted by (Kind,
// Actor, Birth).
type Event struct {
	Kind   Kind
	Actor  population.ActorID
	Birth  int
	Intent uint64
}

// ErrBackwards: a Tick older than the clock.
var ErrBackwards = errors.New("engine: tick moves time backwards")

// Clock is the event queue and the engine's time. Events due at the same
// time come out sorted by (kind, actor, birth), not in push order, so a
// queue rebuilt from a snapshot runs them in the same order as the live one.
type Clock struct {
	queue sched.Queue
	now   sched.Time
	// tickTo is the time of the Tick being handled (or the last one):
	// when the engine runs overdue events, their transactions still go
	// to the node now, so validity intervals start from it (ttl).
	tickTo sched.Time
}

// Push schedules ev at the given time.
func (c *Clock) Push(at sched.Time, ev Event) sched.Handle { return c.queue.Push(at, ev) }

// Cancel removes a scheduled event.
func (c *Clock) Cancel(h sched.Handle) { c.queue.Cancel(h) }

// Peek is the time of the earliest scheduled event.
func (c *Clock) Peek() (sched.Time, bool) { return c.queue.Peek() }

// Now is the time of the event being run, or of the last Tick.
func (c *Clock) Now() sched.Time { return c.now }

// TickTo is the time of the Tick being handled.
func (c *Clock) TickTo() sched.Time { return c.tickTo }

// Load sets the clock to t between inputs, as a snapshot restore does;
// Now is the time a snapshot records.
func (c *Clock) Load(t sched.Time) { c.now, c.tickTo = t, t }

// Open starts a Tick to t. It refuses a time before the clock's.
func (c *Clock) Open(t sched.Time) error {
	if t < c.now {
		return fmt.Errorf("%w: %d < %d", ErrBackwards, t, c.now)
	}
	c.tickTo = t
	return nil
}

// Next pops every event due at the earliest scheduled time at or before t,
// sorted, and moves the clock to that time. Events the batch pushes at the
// same time come out of the following call. With nothing due it moves the
// clock to t and reports false.
func (c *Clock) Next(t sched.Time) (at sched.Time, batch []Event, ok bool) {
	at, ok = c.queue.Peek()
	if !ok || at > t {
		c.now = t
		return 0, nil, false
	}
	for {
		next, ok := c.queue.Peek()
		if !ok || next != at {
			break
		}
		_, ev, _ := c.queue.Pop()
		batch = append(batch, ev.(Event))
	}
	slices.SortFunc(batch, func(a, b Event) int {
		return cmp.Or(cmp.Compare(a.Kind, b.Kind), cmp.Compare(a.Actor, b.Actor), cmp.Compare(a.Birth, b.Birth))
	})
	c.now = at
	return at, batch, true
}
