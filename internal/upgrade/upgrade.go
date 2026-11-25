// Package upgrade schedules an upgrade wave: when a new image is offered,
// pools adopt it along a logistic curve, each at a time drawn from the
// actor's random stream and shifted by the operator's eagerness.
//
// The package is pure. It reads no clock and keeps no state: Plan turns a
// curve, a stream and the pool list into a fixed list of Intents, so the
// same seed always yields the same schedule and a logged wave replays.
//
// The engine owns the rest. It calls Curve.Validate when it accepts a wave,
// calls Plan once at the wave's start with the actor's stream, queues each
// Intent at its At time, and when one fires records an ImageChange event and
// patches the pool's spec.image (the same patch as `continuum image set
// --pool`).
package upgrade

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

// Curve is a logistic adoption curve over [Start, End], normalised so that
// no pool adopts before Start and every pool adopts by End. A raw logistic
// never reaches 0 or 1, which would leave a wave with no last adopter.
//
// Steepness is the logistic's half-width in e-folds: the curve runs from
// sigmoid(-Steepness) at Start to sigmoid(Steepness) at End, so a larger
// value gives a sharper S with a flatter head and tail. The midpoint is
// halfway between Start and End.
type Curve struct {
	Start, End sched.Time
	Steepness  float64
}

// Validate reports why c cannot describe a wave.
func (c Curve) Validate() error {
	switch {
	case c.End <= c.Start:
		return fmt.Errorf("wave ends at %d, not after its start %d", c.End, c.Start)
	case !(c.Steepness > 0) || math.IsInf(c.Steepness, 0):
		return errors.New("wave steepness must be positive and finite")
	}
	return nil
}

func sigmoid(x float64) float64 { return 1 / (1 + math.Exp(-x)) }

// lo and span are the raw logistic's value at Start and its rise to End.
func (c Curve) lo() float64   { return sigmoid(-c.Steepness) }
func (c Curve) span() float64 { return sigmoid(c.Steepness) - c.lo() }

// Fraction is the share of an eagerness-1 population that has adopted by t:
// 0 up to Start, 1 from End, 0.5 at the midpoint, monotone between.
func (c Curve) Fraction(t sched.Time) float64 {
	switch {
	case t <= c.Start:
		return 0
	case t >= c.End:
		return 1
	}
	x := c.Steepness * (2*float64(t-c.Start)/float64(c.End-c.Start) - 1)
	return min(1, max(0, (sigmoid(x)-c.lo())/c.span()))
}

// When is the inverse of Fraction: the time by which share p has adopted.
func (c Curve) When(p float64) sched.Time {
	switch {
	case p <= 0:
		return c.Start
	case p >= 1:
		return c.End
	}
	q := c.lo() + p*c.span()
	x := math.Log(q / (1 - q))
	frac := (x/c.Steepness + 1) / 2
	return c.Start + sched.Time(math.Round(frac*float64(c.End-c.Start)))
}

// AdoptAt is the time a pool adopts, given its draw u in [0, 1) and its
// eagerness e > 0. A population of eagerness 1 with uniform draws follows
// Fraction exactly. Raising u to the power e pulls the draw toward 0, so an
// eager pool (e > 1) adopts earlier and a laggard (e < 1) later, always
// inside [Start, End].
func AdoptAt(c Curve, u, e float64) sched.Time {
	return c.When(math.Pow(u, e))
}

// Source is the random stream a wave draws from. *rand.Rand satisfies it.
type Source interface{ Float64() float64 }

// Pool is a pool taking part in a wave.
type Pool struct {
	ID string
	// Eagerness comes from the pool operator's profile. 1 follows the
	// curve, above 1 adopts sooner, below 1 later. Zero or less never
	// adopts: the operator pins its build.
	Eagerness float64
}

// Intent is one pool's adoption: ImageChange(Pool, Ref) at time At.
type Intent struct {
	At   sched.Time
	Pool string
	Ref  string
}

// String renders the intent as it appears in the event log.
func (i Intent) String() string { return fmt.Sprintf("ImageChange(%s, %s)", i.Pool, i.Ref) }

// Plan returns every adoption of the wave, ordered by time then pool ID.
// Pools are visited in ID order and each consumes one draw whether or not
// it adopts, so the schedule depends only on the stream and the set of
// pools, never on the order they are listed or on another pool's eagerness.
func Plan(c Curve, ref string, pools []Pool, src Source) []Intent {
	sorted := slices.SortedFunc(slices.Values(pools), func(a, b Pool) int { return strings.Compare(a.ID, b.ID) })
	out := make([]Intent, 0, len(sorted))
	for _, p := range sorted {
		u := src.Float64()
		if p.Eagerness <= 0 {
			continue
		}
		out = append(out, Intent{At: AdoptAt(c, u, p.Eagerness), Pool: p.ID, Ref: ref})
	}
	slices.SortStableFunc(out, func(a, b Intent) int {
		if a.At != b.At {
			return int(a.At - b.At)
		}
		return strings.Compare(a.Pool, b.Pool)
	})
	return out
}
