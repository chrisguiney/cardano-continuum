package engine

import (
	"cmp"
	"fmt"
	"slices"

	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

type evKind uint8

// Event kinds in the order same-time events run.
const (
	evBirth evKind = iota + 1
	evHazard
	evAct
	// evRetry only wakes the loop at the end of a backoff; InFlight reads
	// the payment's NotBefore against the clock.
	evRetry
	// evRefill is the faucet's round.
	evRefill
	// evAdopt is one pool's adoption in an upgrade wave; intent indexes
	// the adoptions.
	evAdopt
	// evDeploy opens the next contract.
	evDeploy
)

type qev struct {
	kind   evKind
	actor  population.ActorID
	birth  int
	intent uint64
}

// evClock is the event queue and the engine's time. Events due at the same
// time come out sorted by (kind, actor, birth), not in push order, so a
// queue rebuilt from a snapshot runs them in the same order as the live one.
type evClock struct {
	queue sched.Queue
	now   sched.Time
	// tickTo is the time of the Tick being handled (or the last one):
	// when the engine runs overdue events, their transactions still go
	// to the node now, so validity intervals start from it (ttl).
	tickTo sched.Time
}

// Push schedules ev at the given time.
func (c *evClock) Push(at sched.Time, ev qev) sched.Handle { return c.queue.Push(at, ev) }

// Cancel removes a scheduled event.
func (c *evClock) Cancel(h sched.Handle) { c.queue.Cancel(h) }

// Peek is the time of the earliest scheduled event.
func (c *evClock) Peek() (sched.Time, bool) { return c.queue.Peek() }

// Now is the time of the event being run, or of the last Tick.
func (c *evClock) Now() sched.Time { return c.now }

// TickTo is the time of the Tick being handled.
func (c *evClock) TickTo() sched.Time { return c.tickTo }

// Restore sets the clock to t between inputs, as a snapshot restore does.
func (c *evClock) Restore(t sched.Time) { c.now, c.tickTo = t, t }

// Open starts a Tick to t. It refuses a time before the clock's.
func (c *evClock) Open(t sched.Time) error {
	if t < c.now {
		return fmt.Errorf("%w: %d < %d", ErrTimeBackwards, t, c.now)
	}
	c.tickTo = t
	return nil
}

// Next pops every event due at the earliest scheduled time at or before t,
// sorted, and moves the clock to that time. Events the batch pushes at the
// same time come out of the following call. With nothing due it moves the
// clock to t and reports false.
func (c *evClock) Next(t sched.Time) (at sched.Time, batch []qev, ok bool) {
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
		batch = append(batch, ev.(qev))
	}
	slices.SortFunc(batch, func(a, b qev) int {
		return cmp.Or(cmp.Compare(a.kind, b.kind), cmp.Compare(a.actor, b.actor), cmp.Compare(a.birth, b.birth))
	})
	c.now = at
	return at, batch, true
}
