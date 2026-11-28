package market

import (
	"math"
	"math/rand/v2"
	"strconv"

	"github.com/chrisguiney/cardano-continuum/internal/profile"
	"github.com/chrisguiney/cardano-continuum/internal/rng"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

// block is the steps between two checkpoints. A read recomputes at most a
// block from its checkpoint, and the curve keeps one checkpoint per block,
// so a month of 10-second steps holds about 4000 of them.
const block = 64

// cached is how many recent blocks a curve keeps whole: reads cluster at
// now and one birth window back.
const cached = 4

// point is the state of every path after some steps.
type point struct {
	// ada is log ADA/USD.
	ada float64
	// bull is the ADA drift regime.
	bull bool
	// fx is each currency's log USD rate.
	fx []float64
	// basis is each exchange's log basis.
	basis []float64
}

// cachedBlock is one block's points.
type cachedBlock struct {
	// index is the block's index.
	index int
	// steps are its points; steps[j] is after index*block+j steps.
	steps []point
}

// curve is the walk of every path, memoised: the state after any number of
// steps, recomputed from the nearest checkpoint.
type curve struct {
	// m is the market's parameters.
	m profile.Markets
	// seed is the run seed's market stream.
	seed rng.Seed
	// dt is a step in hours, stepMs in milliseconds.
	dt     float64
	stepMs sched.Time
	// marks are the checkpoints: marks[i] is the state after i*block steps.
	marks []point
	// recent are whole blocks, most recently used last.
	recent []cachedBlock
}

func newCurve(seed rng.Seed, m profile.Markets) *curve {
	start := point{ada: math.Log(m.ADA.Start), bull: true, fx: make([]float64, len(m.Currencies)), basis: make([]float64, len(m.Exchanges))}
	for i, c := range m.Currencies {
		start.fx[i] = math.Log(c.USD)
	}
	return &curve{
		m:      m,
		seed:   seed.Derive("market"),
		dt:     m.Step / 3600,
		stepMs: max(1, sched.Time(math.Round(m.Step*1000))),
		marks:  []point{start},
	}
}

// at is the state at time t: after floor(t/step) steps.
func (c *curve) at(t sched.Time) point {
	k := int(max(0, t) / c.stepMs)
	return c.block(k / block)[k%block]
}

// block returns block i's points, computing the checkpoints up to it.
func (c *curve) block(i int) []point {
	for j, b := range c.recent {
		if b.index == i {
			c.recent = append(append(c.recent[:j:j], c.recent[j+1:]...), b)
			return b.steps
		}
	}

	for len(c.marks) <= i {
		n := len(c.marks) - 1
		pts := c.walk(n, c.marks[n])
		c.marks = append(c.marks, c.step(n*block+block-1, pts[block-1]))
	}

	steps := c.walk(i, c.marks[i])
	if len(c.recent) == cached {
		c.recent = c.recent[1:]
	}
	c.recent = append(c.recent, cachedBlock{index: i, steps: steps})
	return steps
}

// walk returns block i's points from its checkpoint.
func (c *curve) walk(i int, from point) []point {
	out := make([]point, block)
	out[0] = from
	for j := 1; j < block; j++ {
		out[j] = c.step(i*block+j-1, out[j-1])
	}
	return out
}

// step moves the state after k steps to the state after k+1. Its draws are
// step k's own stream, in a fixed order: ADA, regime, jumps, each currency,
// each exchange.
func (c *curve) step(k int, s point) point {
	r := c.seed.Derive(strconv.Itoa(k)).Rand()
	a := c.m.ADA
	drift := a.Drift
	if !s.bull {
		drift = -drift
	}
	next := point{
		ada:   s.ada + (drift-a.Vol*a.Vol/2)*c.dt + a.Vol*math.Sqrt(c.dt)*r.NormFloat64(),
		bull:  s.bull != (r.Float64() < 1-math.Exp(-a.RegimeRate*c.dt)),
		fx:    make([]float64, len(s.fx)),
		basis: make([]float64, len(s.basis)),
	}
	if n := poisson(r, a.JumpRate*c.dt); n > 0 {
		next.ada += float64(n)*a.JumpMean + a.JumpSD*math.Sqrt(float64(n))*r.NormFloat64()
	}

	for i, cur := range c.m.Currencies {
		next.fx[i] = ou(r, s.fx[i], math.Log(cur.USD), c.m.FX.Reversion, c.m.FX.Vol, c.dt)
	}

	for i, x := range c.m.Exchanges {
		next.basis[i] = ou(r, s.basis[i], 0, x.BasisReversion, x.BasisVol, c.dt)
	}
	return next
}

// ou is one exact step of an Ornstein-Uhlenbeck process from x toward
// level, so a coarse step neither overshoots nor loses variance.
func ou(r *rand.Rand, x, level, theta, vol, dt float64) float64 {
	if theta == 0 {
		return x + vol*math.Sqrt(dt)*r.NormFloat64()
	}
	a := math.Exp(-theta * dt)
	return level + (x-level)*a + vol*math.Sqrt((1-a*a)/(2*theta))*r.NormFloat64()
}

// poisson draws a Poisson count of mean lambda by inversion, which is exact
// and cheap for the small means a step has. It always takes one draw.
func poisson(r *rand.Rand, lambda float64) int {
	u := r.Float64()
	p := math.Exp(-lambda)
	cum, n := p, 0
	for u > cum && n < 64 {
		n++
		p *= lambda / float64(n)
		cum += p
	}
	return n
}
