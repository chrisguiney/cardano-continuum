package sched

import (
	"fmt"
	"math"
	"math/rand/v2"
)

const (
	msPerHour = 3600_000
	msPerDay  = 24 * msPerHour
	msPerWeek = 7 * msPerDay
)

// Modulation scales a base arrival rate over time. Factor must stay within
// [0, Max()]; Max is the bound the thinning sampler relies on.
type Modulation interface {
	Factor(t Time) float64
	Max() float64
}

// SquareWave is on (factor 1) for the first OnFraction of each Period and
// at Floor for the rest.
type SquareWave struct {
	Period     Time
	OnFraction float64
	Floor      float64
}

// Factor implements Modulation.
func (s SquareWave) Factor(t Time) float64 {
	if s.Period <= 0 {
		return 1
	}
	phase := float64(posMod(t, s.Period)) / float64(s.Period)
	if phase < s.OnFraction {
		return 1
	}
	return s.Floor
}

// Max implements Modulation.
func (SquareWave) Max() float64 { return 1 }

// Daily is a 24h sinusoid 1 + Amplitude*sin(2*pi*t/day), peaking six hours
// after system start. Amplitude must be in [0, 1] to keep Factor
// non-negative.
type Daily struct{ Amplitude float64 }

// Factor implements Modulation.
func (d Daily) Factor(t Time) float64 {
	return 1 + d.Amplitude*math.Sin(2*math.Pi*float64(posMod(t, msPerDay))/msPerDay)
}

// Max implements Modulation.
func (d Daily) Max() float64 { return 1 + d.Amplitude }

// Weekly runs at factor 1 on days 0-4 of each week and at Weekend on days
// 5 and 6, counting from system start.
type Weekly struct{ Weekend float64 }

// Factor implements Modulation.
func (w Weekly) Factor(t Time) float64 {
	if posMod(t, msPerWeek) >= 5*msPerDay {
		return w.Weekend
	}
	return 1
}

// Max implements Modulation.
func (Weekly) Max() float64 { return 1 }

// Product multiplies several modulations.
type Product []Modulation

// Factor implements Modulation.
func (p Product) Factor(t Time) float64 {
	f := 1.0
	for _, m := range p {
		f *= m.Factor(t)
	}
	return f
}

// Max implements Modulation.
func (p Product) Max() float64 {
	f := 1.0
	for _, m := range p {
		f *= m.Max()
	}
	return f
}

func posMod(t, m Time) Time {
	r := t % m
	if r < 0 {
		r += m
	}
	return r
}

// never is returned when no further arrival will occur.
const never = Time(math.MaxInt64)

// NextArrival samples the next arrival of a Poisson process with base rate
// per second, scaled by mod (nil means constant), strictly after now. It uses
// Lewis-Shedler thinning against mod.Max(). The result is rounded up to a whole
// millisecond and is always at least now+1.
func NextArrival(r *rand.Rand, rate float64, mod Modulation, now Time) Time {
	if rate <= 0 {
		return never
	}
	bound := rate
	if mod != nil {
		bound *= mod.Max()
	}
	if bound <= 0 {
		return never
	}
	x := float64(now)
	for {
		x += r.ExpFloat64() / bound * 1000
		if x >= float64(never) {
			return never
		}
		if mod == nil {
			break
		}
		if r.Float64()*mod.Max() < mod.Factor(Time(x)) {
			break
		}
	}
	return max(now+1, Time(math.Ceil(x)))
}

// Hawkes is a self-exciting process with an exponential kernel: intensity
// (events per second) is Mu plus Alpha*exp(-Beta*dt) for each past event.
// The branching ratio Alpha/Beta is below 1, so bursts die out. A Hawkes is
// stateful and not safe for concurrent use.
type Hawkes struct {
	Mu, Alpha, Beta float64

	last   Time    // time of the last Excite or Next
	excess float64 // intensity above Mu at last
}

// NewHawkes validates the parameters: Mu > 0, Alpha >= 0, Beta > 0 and
// Alpha < Beta (otherwise the process is explosive).
func NewHawkes(mu, alpha, beta float64) (*Hawkes, error) {
	switch {
	case !(mu > 0):
		return nil, fmt.Errorf("hawkes: mu %v must be positive", mu)
	case !(alpha >= 0):
		return nil, fmt.Errorf("hawkes: alpha %v must be non-negative", alpha)
	case !(beta > 0):
		return nil, fmt.Errorf("hawkes: beta %v must be positive", beta)
	case alpha >= beta:
		return nil, fmt.Errorf("hawkes: alpha %v >= beta %v is explosive", alpha, beta)
	}
	return &Hawkes{Mu: mu, Alpha: alpha, Beta: beta}, nil
}

func (h *Hawkes) at(ms float64) float64 {
	dt := ms - float64(h.last)
	if dt < 0 {
		dt = 0
	}
	return h.Mu + h.excess*math.Exp(-h.Beta*dt/1000)
}

// Intensity returns the current event rate per second at now. Times before
// the last recorded event are treated as that event's time.
func (h *Hawkes) Intensity(now Time) float64 { return h.at(float64(now)) }

// Excite records an event at the given time, adding Alpha to the intensity.
func (h *Hawkes) Excite(at Time) {
	h.excess = h.at(float64(at)) - h.Mu + h.Alpha
	h.last = max(at, h.last)
}

// Next samples the next event strictly after now by Ogata thinning, records
// it with Excite and returns its time. Intensity only decays between
// events, so its value at the candidate origin bounds the thinning.
func (h *Hawkes) Next(r *rand.Rand, now Time) Time {
	x := float64(now)
	for {
		bound := h.at(x)
		x += r.ExpFloat64() / bound * 1000
		if r.Float64()*bound <= h.at(x) {
			break
		}
	}
	t := max(now+1, Time(math.Ceil(x)))
	h.Excite(t)
	return t
}

// Excitation is the self-excited part of a Hawkes intensity as a value:
// Excess events per second above the base rate, as of time Since. It
// decays at the process's beta and gains alpha per event. Two numbers are
// the whole state of an exponential kernel, so it fits in a snapshot.
type Excitation struct {
	Excess float64
	Since  Time
}

// At is the excess at t, decayed at beta per second; times before Since
// read as Since.
func (x Excitation) At(t Time, beta float64) float64 {
	if x.Excess == 0 {
		return 0
	}
	dt := max(0, float64(t-x.Since)/1000)
	return x.Excess * math.Exp(-beta*dt)
}

// Excite records an event at t.
func (x Excitation) Excite(t Time, alpha, beta float64) Excitation {
	return Excitation{Excess: x.At(t, beta) + alpha, Since: max(t, x.Since)}
}

// NextExcited samples the next arrival strictly after now of a modulated
// Hawkes process: intensity mod(t) * (mu + x.At(t)), mod nil meaning 1.
// It thins against mod.Max() times the intensity at the candidate's
// origin, which bounds what follows because the excess only decays
// between events (Ogata). The caller records the arrival with Excite. The
// long-run rate is mu / (1 - alpha/beta) times the modulation's mean.
func NextExcited(r *rand.Rand, mu, beta float64, x Excitation, mod Modulation, now Time) Time {
	peak := 1.0
	if mod != nil {
		peak = mod.Max()
	}
	t := float64(now)
	for {
		bound := peak * (mu + x.At(Time(t), beta))
		if !(bound > 0) {
			return never
		}
		t += r.ExpFloat64() / bound * 1000
		if t >= float64(never) {
			return never
		}
		f := 1.0
		if mod != nil {
			f = mod.Factor(Time(t))
		}
		if r.Float64()*bound < f*(mu+x.At(Time(t), beta)) {
			break
		}
	}
	return max(now+1, Time(math.Ceil(t)))
}
