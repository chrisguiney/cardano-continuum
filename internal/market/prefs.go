package market

import (
	"math"
	"math/rand/v2"
	"slices"

	"github.com/chrisguiney/cardano-continuum/internal/profile"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

// Order is what a wallet wants to trade, for choosing its venue.
type Order struct {
	// Buy is a purchase of ADA; false is a sale.
	Buy bool
	// Primary is the wallet's primary currency, Extra its others.
	Primary int
	Extra   []int
	// At is the time of the order.
	At sched.Time
	// Last is the exchange the wallet last traded on, or -1.
	Last int
}

// Venue is where an order trades.
type Venue struct {
	// Exchange and Currency are the venue's indexes.
	Exchange int
	Currency int
	// Rate is the venue's ask (a buy) or bid (a sale) in Currency per ADA.
	Rate float64
	// Primary is the price per ADA in the wallet's primary currency,
	// conversion included.
	Primary float64
}

// births is Path.Birth as a sched.Modulation.
type births struct {
	// p is the market's paths.
	p *Path
	// c is the currency.
	c int
}

// Factor implements sched.Modulation.
func (b births) Factor(t sched.Time) float64 { return b.p.Birth(b.c, t) }

// Max implements sched.Modulation.
func (b births) Max() float64 { return b.p.c.m.Births.Max }

// deaths is Path.Death as a sched.Modulation.
type deaths struct {
	// p is the market's paths.
	p *Path
	// c is the currency.
	c int
	// hodl damps the factor.
	hodl bool
}

// Factor implements sched.Modulation.
func (d deaths) Factor(t sched.Time) float64 { return d.p.Death(d.c, t, d.hodl) }

// Max implements sched.Modulation.
func (d deaths) Max() float64 {
	if d.hodl {
		return math.Pow(d.p.c.m.Deaths.Max, d.p.c.m.Deaths.Hodl)
	}
	return d.p.c.m.Deaths.Max
}

// byShare picks the currency at u in [0, 1) of the shares of those not
// taken. A u past the last share, from rounding, picks the last one left.
func byShare(cs []profile.Currency, u float64, taken []int) int {
	var total float64
	for i, c := range cs {
		if !slices.Contains(taken, i) {
			total += c.Share
		}
	}

	u *= total
	last := -1
	for i, c := range cs {
		if slices.Contains(taken, i) {
			continue
		}
		last = i
		if u < c.Share {
			return i
		}
		u -= c.Share
	}
	return last
}

// gumbel draws a standard Gumbel variate: the noise that makes a choice by
// best cost a logit choice.
func gumbel(r *rand.Rand) float64 {
	return -math.Log(-math.Log(max(r.Float64(), math.SmallestNonzeroFloat64)))
}
