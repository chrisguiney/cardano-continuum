package population

import (
	"math"
	"math/rand/v2"

	"github.com/chrisguiney/cardano-continuum/internal/profile"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

// Never is the time of an event that will not happen.
const Never = sched.Time(math.MaxInt64)

const (
	// frustrationGain is how many times frustration 1 multiplies the onset.
	frustrationGain = 4.0

	// maxHorizonSeconds bounds a sampled delay so the millisecond conversion
	// cannot overflow.
	maxHorizonSeconds = 1e12
)

// DormancyOnsetRate is the per-second hazard that an active wallet goes
// dormant at the given frustration, from the profile's unfrustrated rate
// base (profile.Hazards.OnsetRate). It is strictly increasing in
// frustration; negative frustration is treated as zero.
func DormancyOnsetRate(base, frustration float64) float64 {
	return base * (1 + frustrationGain*max(frustration, 0))
}

// abandonRate converts the profile's per-second abandon probability into an
// exponential hazard rate.
func abandonRate(p float64) float64 {
	if p <= 0 {
		return 0
	}
	if p >= 1 {
		return math.Inf(1)
	}
	return -math.Log1p(-p)
}

// after returns now plus a delay in seconds, at least one millisecond later.
func after(now sched.Time, seconds float64) sched.Time {
	if seconds >= maxHorizonSeconds || math.IsNaN(seconds) {
		return Never
	}
	ms := sched.Time(math.Ceil(seconds * 1000))
	if ms < 1 {
		ms = 1
	}
	if ms > Never-now {
		return Never
	}
	return now + ms
}

// NextHazard returns when the actor's own clock next moves it and to which
// state, or (Never, a.State) if nothing is pending. Only Timer-caused edges
// appear here: pools, DReps and deployed contracts move on chain events. Time
// units are seconds, matching the profile rates.
//
//   - Born wallets become Active one millisecond later.
//   - Active wallets race dormancy onset (exponential, raised by frustration,
//     off if the profile has no dormancy distribution) against abandonment.
//   - Dormant wallets race the end of a dormancy (a draw from the profile's
//     distribution) against abandonment.
//   - Used contracts are abandoned at the profile's abandon hazard.
//
// Each competing delay is drawn on every call whenever it is enabled, so the
// number of draws depends on the profile and state but not on the outcome.
// Ties go to the first racer listed above.
func NextHazard(a Actor, p profile.Profile, r *rand.Rand, now sched.Time) (sched.Time, State) {
	return NextHazardIn(a, p, r, now, nil)
}

// NextHazardIn is NextHazard with an active or dormant wallet's dormancy
// onset and abandonment hazards scaled by mod, sampled by thinning
// (sched.NextArrival); a dormancy's end is not scaled. A nil mod draws
// exactly as NextHazard always has.
func NextHazardIn(a Actor, p profile.Profile, r *rand.Rand, now sched.Time, mod sched.Modulation) (sched.Time, State) {
	hold := func() (sched.Time, State) { return Never, a.State }
	switch {
	case a.Kind == profile.Wallet && a.State == Born:
		return now + 1, Active
	case a.Kind == profile.Wallet && a.State == Active:
		return race(r, now, a.State, [2]racer{
			{Dormant, scaled(onset(a.Frustration, p), DormancyOnsetRate(p.Hazards.OnsetRate(), a.Frustration), mod, now)},
			{Abandoned, scaled(abandon(p), abandonRate(p.Hazards.Abandon), mod, now)},
		})
	case a.Kind == profile.Wallet && a.State == Dormant:
		var back func(*rand.Rand) float64
		if p.Hazards.Dormancy != nil {
			back = func(r *rand.Rand) float64 { return p.Hazards.Dormancy.Sample(r) }
		}
		return race(r, now, a.State, [2]racer{
			{Active, back},
			{Abandoned, scaled(abandon(p), abandonRate(p.Hazards.Abandon), mod, now)},
		})
	case a.Kind == profile.Contract && a.State == Used:
		return race(r, now, a.State, [2]racer{{Abandoned, abandon(p)}, {}})
	}
	return hold()
}

// scaled is draw with its constant rate scaled by mod from now: the delay
// to a thinned arrival at rate times mod. A disabled draw stays disabled,
// and a nil mod leaves the draw as it is.
func scaled(draw func(*rand.Rand) float64, rate float64, mod sched.Modulation, now sched.Time) func(*rand.Rand) float64 {
	if draw == nil || mod == nil || math.IsInf(rate, 1) {
		return draw
	}
	return func(r *rand.Rand) float64 {
		at := sched.NextArrival(r, rate, mod, now)
		if at == Never {
			return math.Inf(1)
		}
		return float64(at-now) / 1000
	}
}

type racer struct {
	to   State
	draw func(*rand.Rand) float64 // seconds; nil means disabled
}

func race(r *rand.Rand, now sched.Time, stay State, rs [2]racer) (sched.Time, State) {
	best, to := math.Inf(1), stay
	for _, c := range rs {
		if c.draw == nil {
			continue
		}
		if d := c.draw(r); d < best {
			best, to = d, c.to
		}
	}
	if math.IsInf(best, 1) {
		return Never, stay
	}
	return after(now, best), to
}

func onset(frustration float64, p profile.Profile) func(*rand.Rand) float64 {
	if p.Hazards.Dormancy == nil {
		return nil
	}
	return expDraw(DormancyOnsetRate(p.Hazards.OnsetRate(), frustration))
}

func abandon(p profile.Profile) func(*rand.Rand) float64 {
	return expDraw(abandonRate(p.Hazards.Abandon))
}

func expDraw(rate float64) func(*rand.Rand) float64 {
	switch {
	case rate <= 0:
		return nil
	case math.IsInf(rate, 1):
		return func(*rand.Rand) float64 { return 0 }
	}
	return func(r *rand.Rand) float64 { return profile.Exp{Rate: rate}.Sample(r) }
}

// NextArrival returns the next birth of a stream at rate per second scaled
// by mod, sampled by thinning (sched.NextArrival), or Never at rate zero.
func NextArrival(r *rand.Rand, rate float64, mod sched.Modulation, now sched.Time) sched.Time {
	if rate <= 0 {
		return Never
	}
	return sched.NextArrival(r, rate, mod, now)
}

// NextBirth returns when the next actor of profile p is born after now, a
// Poisson arrival at the profile's birth rate per second, or Never if the
// rate is zero. r should be a stream dedicated to that profile, such as
// root.Derive("birth", p.Name).Rand().
func NextBirth(r *rand.Rand, p profile.Profile, now sched.Time) sched.Time {
	if p.Hazards.Birth <= 0 {
		return Never
	}
	return sched.NextArrival(r, p.Hazards.Birth, nil, now)
}
