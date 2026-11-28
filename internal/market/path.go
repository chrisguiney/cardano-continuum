package market

import (
	"math"
	"math/rand/v2"

	"github.com/chrisguiney/cardano-continuum/internal/profile"
	"github.com/chrisguiney/cardano-continuum/internal/rng"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

// Path is the market's price paths over one run and the quotes, trends and
// venues read from them. Currencies and exchanges are indexes into the
// parameters' lists.
type Path struct {
	// c is the memoised walk.
	c *curve
	// codes indexes the currencies by code.
	codes map[string]int
	// quotes is, per exchange, which currencies it quotes.
	quotes []map[int]bool
}

// New returns the market's paths for a run seeded by seed.
func New(seed rng.Seed, m profile.Markets) *Path {
	codes := make(map[string]int, len(m.Currencies))
	for i, c := range m.Currencies {
		codes[c.Code] = i
	}

	quotes := make([]map[int]bool, len(m.Exchanges))
	for i, x := range m.Exchanges {
		quotes[i] = map[int]bool{}
		for _, c := range x.Currencies {
			quotes[i][codes[c]] = true
		}
	}
	return &Path{c: newCurve(seed, m), codes: codes, quotes: quotes}
}

// Params are the market's parameters.
func (p *Path) Params() profile.Markets { return p.c.m }

// Currency is the index of the currency with code, or -1.
func (p *Path) Currency(code string) int {
	if i, ok := p.codes[code]; ok {
		return i
	}
	return -1
}

// ADA is ADA/USD at t.
func (p *Path) ADA(t sched.Time) float64 { return math.Exp(p.c.at(t).ada) }

// USD is the USD one unit of currency c buys at t.
func (p *Path) USD(c int, t sched.Time) float64 { return math.Exp(p.c.at(t).fx[c]) }

// Mid is the mid rate of ADA in currency c at t: units of c per ADA.
func (p *Path) Mid(c int, t sched.Time) float64 {
	s := p.c.at(t)
	return math.Exp(s.ada - s.fx[c])
}

// Quote is exchange x's bid and ask for ADA in currency c at t, in units of
// c per ADA; ok is false when x does not quote c.
func (p *Path) Quote(x, c int, t sched.Time) (bid, ask float64, ok bool) {
	if !p.quotes[x][c] {
		return 0, 0, false
	}
	s := p.c.at(t)
	mid := math.Exp(s.ada - s.fx[c] + s.basis[x])
	half := p.c.m.Exchanges[x].Spread / 2
	return mid * (1 - half), mid * (1 + half), true
}

// Trend is the log change of ADA in currency c over the birth window
// ending at t. Before a whole window has passed it reads from time zero.
func (p *Path) Trend(c int, t sched.Time) float64 {
	back := t - sched.Time(math.Round(p.c.m.Births.Window*1000))
	return math.Log(p.Mid(c, t) / p.Mid(c, max(0, back)))
}

// Birth is the factor ADA's trend in currency c puts on that currency's
// birth hazard at t: exp(beta * trend), clamped to the births bounds.
func (p *Path) Birth(c int, t sched.Time) float64 {
	b := p.c.m.Births
	return min(b.Max, max(b.Min, math.Exp(b.Beta*p.Trend(c, t))))
}

// Death is the factor ADA's fall in currency c over the death window puts
// on an opted-in wallet's dormancy and abandonment hazards at t, damped
// for a hodl wallet.
func (p *Path) Death(c int, t sched.Time, hodl bool) float64 {
	d := p.c.m.Deaths
	back := t - sched.Time(math.Round(d.Window*1000))
	f := min(d.Max, max(d.Min, math.Exp(-d.Beta*math.Log(p.Mid(c, t)/p.Mid(c, max(0, back))))))
	if hodl {
		return math.Pow(f, d.Hodl)
	}
	return f
}

// Deaths is Death as a modulation for thinning.
func (p *Path) Deaths(c int, hodl bool) sched.Modulation { return deaths{p: p, c: c, hodl: hodl} }

// Births is currency c's birth factor as a modulation for thinning.
func (p *Path) Births(c int) sched.Modulation { return births{p: p, c: c} }

// Primary draws a wallet's primary currency by share. It takes one draw.
func (p *Path) Primary(r *rand.Rand) int { return byShare(p.c.m.Currencies, r.Float64(), nil) }

// Extras draws a wallet's further preferred currencies, distinct and never
// primary: each one more with probability Extra, so the count is
// geometric. Each step takes one draw, and each currency added one more.
func (p *Path) Extras(r *rand.Rand, primary int) []int {
	cs := p.c.m.Currencies
	var out []int
	taken := []int{primary}

	for len(taken) < len(cs) && r.Float64() < p.c.m.Extra {
		c := byShare(cs, r.Float64(), taken)
		out = append(out, c)
		taken = append(taken, c)
	}
	return out
}

// Venue picks the order's exchange and currency: the best effective price
// in the primary currency, a non-primary currency paying the conversion,
// the last exchange discounted by loyalty, and Gumbel noise. It draws once
// per candidate, exchanges in order and each exchange's currencies in the
// wallet's order. ok is false when no exchange quotes the wallet's
// currencies.
func (p *Path) Venue(r *rand.Rand, o Order) (Venue, bool) {
	if o.Primary < 0 {
		return Venue{}, false
	}
	held := append([]int{o.Primary}, o.Extra...)
	var best Venue
	bestCost, found := math.Inf(1), false

	for x := range p.c.m.Exchanges {
		for _, c := range held {
			cand, cost, ok := p.offer(o, x, c)
			if !ok {
				continue
			}
			if cost -= p.c.m.Venue.Noise * gumbel(r); cost < bestCost {
				best, bestCost, found = cand, cost, true
			}
		}
	}
	return best, found
}

// offer is exchange x's price for the order in currency c, and its cost to
// the wallet in log units: lower is better for a buy and a sale alike.
func (p *Path) offer(o Order, x, c int) (Venue, float64, bool) {
	bid, ask, ok := p.Quote(x, c, o.At)
	if !ok {
		return Venue{}, 0, false
	}
	conv := 1.0
	if c != o.Primary {
		conv = 1 + p.c.m.Venue.Conversion
	}
	fx := p.USD(c, o.At) / p.USD(o.Primary, o.At)
	v := Venue{Exchange: x, Currency: c, Rate: ask, Primary: ask * fx * conv}
	cost := math.Log(v.Primary)
	if !o.Buy {
		v.Rate, v.Primary = bid, bid*fx/conv
		cost = -math.Log(v.Primary)
	}
	if x == o.Last {
		cost -= p.c.m.Venue.Loyalty
	}
	return v, cost, true
}
