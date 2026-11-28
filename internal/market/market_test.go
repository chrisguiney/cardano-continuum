package market

import (
	"math"
	"math/rand/v2"
	"testing"

	"github.com/chrisguiney/cardano-continuum/internal/profile"
	"github.com/chrisguiney/cardano-continuum/internal/rng"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

func seed(n uint64) rng.Seed {
	var s rng.Seed
	s[0] = byte(n)
	return s.Derive("test")
}

func path(n uint64) *Path { return New(seed(n), profile.DefaultMarkets()) }

const hour = sched.Time(3_600_000)

// The path is a function of seed and time alone: two paths of one seed agree
// at every time whatever order they are read in, and another seed differs.
func TestPathIsPureInSeedAndTime(t *testing.T) {
	a, b := path(1), path(1)
	times := []sched.Time{0, 9_999, 10_000, 4 * hour, 7_000, hour, 3 * hour, 12 * hour}
	for _, at := range times {
		if a.ADA(at) <= 0 {
			t.Fatalf("ADA(%d) = %v", at, a.ADA(at))
		}
	}
	for i := len(times) - 1; i >= 0; i-- {
		if x, y := a.ADA(times[i]), b.ADA(times[i]); x != y {
			t.Fatalf("ADA(%d) = %v and %v from one seed", times[i], x, y)
		}
	}
	if path(2).ADA(hour) == a.ADA(hour) {
		t.Fatal("two seeds give one path")
	}
}

func TestPathStartsAtStartAndStepsPiecewise(t *testing.T) {
	p := path(1)
	if got := p.ADA(0); got != profile.DefaultMarkets().ADA.Start {
		t.Fatalf("ADA(0) = %v, want start", got)
	}
	if p.ADA(10_000) != p.ADA(19_999) {
		t.Fatal("the path moves inside a step")
	}
	if p.ADA(9_999) != p.ADA(0) {
		t.Fatal("the path moves before the first step ends")
	}
}

// Over many hours the walk is volatile enough to matter: in a day it sees a
// drawdown of at least 10% from some peak.
func TestPathHasDrawdowns(t *testing.T) {
	p := path(3)
	peak, worst := 0.0, 0.0
	for at := sched.Time(0); at < 24*hour; at += 60_000 {
		x := p.ADA(at)
		peak = max(peak, x)
		worst = min(worst, x/peak-1)
	}
	if worst > -0.1 {
		t.Fatalf("worst drawdown %.3f in a day, want below -0.1", worst)
	}
}

func TestQuotes(t *testing.T) {
	p := path(1)
	m := profile.DefaultMarkets()
	at := 2 * hour
	for ci, c := range m.Currencies {
		mid := p.Mid(ci, at)
		if want := p.ADA(at) / p.USD(ci, at); math.Abs(mid/want-1) > 1e-12 {
			t.Fatalf("%s mid %v, want ADA/USD over USD per unit %v", c.Code, mid, want)
		}
	}
	for xi, x := range m.Exchanges {
		for _, code := range x.Currencies {
			ci := p.Currency(code)
			bid, ask, ok := p.Quote(xi, ci, at)
			if !ok {
				t.Fatalf("%s does not quote %s", x.Name, code)
			}
			if !(bid < ask) || math.Abs(ask/bid-(1+x.Spread/2)/(1-x.Spread/2)) > 1e-9 {
				t.Fatalf("%s %s bid %v ask %v spread %v", x.Name, code, bid, ask, x.Spread)
			}
			if mid := p.Mid(ci, at); bid > mid*1.05 || ask < mid*0.95 {
				t.Fatalf("%s %s quotes %v/%v far from mid %v", x.Name, code, bid, ask, mid)
			}
		}
	}
	if _, _, ok := p.Quote(0, p.Currency("KRW"), at); ok {
		t.Fatal("north quotes KRW")
	}
	if p.Currency("XXX") != -1 {
		t.Fatal("unknown code has an index")
	}
}

// Exchanges quote the same currency at slightly different rates.
func TestExchangesDiffer(t *testing.T) {
	p := path(1)
	usd := p.Currency("USD")
	b0, _, _ := p.Quote(0, usd, 3*hour)
	b1, _, _ := p.Quote(1, usd, 3*hour)
	b2, _, _ := p.Quote(2, usd, 3*hour)
	if b0 == b1 || b1 == b2 {
		t.Fatalf("bids %v %v %v: exchanges agree exactly", b0, b1, b2)
	}
}

func TestBirthFactor(t *testing.T) {
	p := path(1)
	b := profile.DefaultMarkets().Births
	usd := p.Currency("USD")
	for at := sched.Time(0); at < 6*hour; at += 30_000 {
		f := p.Birth(usd, at)
		if f < b.Min || f > b.Max {
			t.Fatalf("factor %v at %d outside [%v, %v]", f, at, b.Min, b.Max)
		}
		want := math.Exp(b.Beta * p.Trend(usd, at))
		if want >= b.Min && want <= b.Max && math.Abs(f-want) > 1e-12 {
			t.Fatalf("factor %v, want exp(beta*trend) %v", f, want)
		}
	}
	if f := p.Birth(usd, 0); f != 1 {
		t.Fatalf("factor at time zero %v, want 1 (no history)", f)
	}
	mod := p.Births(usd)
	if mod.Max() != b.Max || mod.Factor(hour) != p.Birth(usd, hour) {
		t.Fatal("Births is not the factor as a modulation")
	}
}

// Primary currencies follow the shares; extra currencies are distinct, never
// the primary, and geometric: most wallets have none, fewer each step.
func TestPrefs(t *testing.T) {
	p := path(1)
	m := profile.DefaultMarkets()
	r := rand.New(rand.NewPCG(1, 2))
	counts := make([]int, len(m.Currencies))
	extras := map[int]int{}
	const n = 20000
	for range n {
		pri := p.Primary(r)
		counts[pri]++
		ex := p.Extras(r, pri)
		extras[len(ex)]++
		seen := map[int]bool{pri: true}
		for _, c := range ex {
			if seen[c] {
				t.Fatalf("extras %v repeat or hold primary %d", ex, pri)
			}
			seen[c] = true
		}
	}
	for i, c := range m.Currencies {
		if got := float64(counts[i]) / n; math.Abs(got-c.Share) > 0.015 {
			t.Errorf("%s share %.3f, want %.3f", c.Code, got, c.Share)
		}
	}
	if extras[0] <= extras[1] || extras[1] <= extras[2] || extras[2] == 0 {
		t.Fatalf("extra counts %v not geometric", extras)
	}
	if got := float64(extras[0]) / n; math.Abs(got-(1-m.Extra)) > 0.015 {
		t.Fatalf("no-extra share %.3f, want %.3f", got, 1-m.Extra)
	}
}

// A wallet trades where its effective price is best: with no noise and no
// loyalty a buyer takes the cheapest ask in its primary currency, a seller
// the best bid; loyalty can keep it at a slightly worse venue.
func TestVenue(t *testing.T) {
	m := profile.DefaultMarkets()
	m.Venue.Noise, m.Venue.Loyalty = 0, 0
	p := New(seed(1), m)
	r := rand.New(rand.NewPCG(1, 2))
	usd := p.Currency("USD")
	at := 2 * hour
	v, ok := p.Venue(r, Order{Buy: true, Primary: usd, At: at, Last: -1})
	if !ok {
		t.Fatal("no venue for a USD buyer")
	}
	best := math.Inf(1)
	for xi := range m.Exchanges {
		if _, ask, ok := p.Quote(xi, usd, at); ok {
			best = min(best, ask)
		}
	}
	if v.Currency != usd || v.Rate != best {
		t.Fatalf("buyer venue %+v, want the cheapest USD ask %v", v, best)
	}
	s, _ := p.Venue(r, Order{Primary: usd, At: at, Last: -1})
	for xi := range m.Exchanges {
		if bid, _, ok := p.Quote(xi, usd, at); ok && bid > s.Rate {
			t.Fatalf("seller venue %+v, exchange %d bids %v", s, xi, bid)
		}
	}
	if _, ok := p.Venue(r, Order{Buy: true, Primary: -1, At: at, Last: -1}); ok {
		t.Fatal("venue without a currency")
	}

	m.Venue.Loyalty = 1
	p = New(seed(1), m)
	worst := (v.Exchange + 1) % len(m.Exchanges)
	if _, _, ok := p.Quote(worst, usd, at); !ok {
		t.Skip("next exchange does not quote USD")
	}
	l, _ := p.Venue(r, Order{Buy: true, Primary: usd, At: at, Last: worst})
	if l.Exchange != worst {
		t.Fatalf("loyal buyer left %d for %d", worst, l.Exchange)
	}
}

// A non-primary currency costs the conversion: a JPY holder whose only
// venue in JPY is worse after conversion still compares it fairly.
func TestVenueConvertsToPrimary(t *testing.T) {
	m := profile.DefaultMarkets()
	m.Venue.Noise, m.Venue.Loyalty = 0, 0
	p := New(seed(1), m)
	r := rand.New(rand.NewPCG(1, 2))
	jpy, krw := p.Currency("JPY"), p.Currency("KRW")
	v, ok := p.Venue(r, Order{Buy: true, Primary: jpy, Extra: []int{krw}, At: hour, Last: -1})
	if !ok || v.Currency != jpy {
		t.Fatalf("venue %+v: the conversion makes the primary cheaper", v)
	}
	if want := v.Rate; math.Abs(v.Primary-want) > 1e-12*want {
		t.Fatalf("primary price %v, want the JPY ask %v", v.Primary, want)
	}
}

// The death factor rises as ADA falls over the death window, mirrors the
// birth factor's sign, and a hodl wallet feels a damped one.
func TestDeathFactor(t *testing.T) {
	p := path(3)
	d := profile.DefaultMarkets().Deaths
	usd := p.Currency("USD")
	window := sched.Time(d.Window * 1000)
	rose, fell := false, false
	for at := window; at < 24*hour; at += 60_000 {
		trend := math.Log(p.Mid(usd, at) / p.Mid(usd, at-window))
		f, h := p.Death(usd, at, false), p.Death(usd, at, true)
		if f < d.Min || f > d.Max {
			t.Fatalf("factor %v outside bounds", f)
		}
		if want := math.Exp(-d.Beta * trend); want > d.Min && want < d.Max && math.Abs(f-want) > 1e-9 {
			t.Fatalf("factor %v, want %v", f, want)
		}
		if f > 1 && !(h < f && h > 1) || f < 1 && !(h > f && h < 1) {
			t.Fatalf("hodl factor %v does not damp %v", h, f)
		}
		rose, fell = rose || f > 1.5, fell || f < 0.8
	}
	if !rose || !fell {
		t.Fatal("the death factor never moved both ways in a day")
	}
	if m := p.Deaths(usd, true); m.Max() != math.Pow(d.Max, d.Hodl) || m.Factor(hour) != p.Death(usd, hour, true) {
		t.Fatal("Deaths is not the factor as a modulation")
	}
}

// An exchange that cannot pay is no venue for a buyer: Able removes it
// from the candidates, and no able exchange leaves no venue.
func TestVenueSkipsUnableExchange(t *testing.T) {
	m := profile.DefaultMarkets()
	m.Venue.Noise, m.Venue.Loyalty = 0, 0
	p := New(seed(1), m)
	r := rand.New(rand.NewPCG(1, 2))
	usd := p.Currency("USD")
	o := Order{Buy: true, Primary: usd, At: 2 * hour, Last: -1}
	v, ok := p.Venue(r, o)
	if !ok {
		t.Fatal("no venue for a USD buyer")
	}

	o.Able = func(x int) bool { return x != v.Exchange }
	got, ok := p.Venue(r, o)
	if ok && got.Exchange == v.Exchange {
		t.Fatalf("venue %d chosen though it cannot pay", got.Exchange)
	}

	o.Able = func(int) bool { return false }
	if _, ok := p.Venue(r, o); ok {
		t.Fatal("a venue with no able exchange")
	}
}

func logUSD(p *Path, code string, at sched.Time) float64 {
	for i, c := range profile.DefaultMarkets().Currencies {
		if c.Code == code {
			return math.Log(p.USD(i, at))
		}
	}
	panic("no currency " + code)
}

// stepSD is the standard deviation of a currency's log change per step over
// a day.
func stepSD(p *Path, code string) float64 {
	var sum, sq float64
	n := 0
	prev := logUSD(p, code, 0)
	for at := sched.Time(10_000); at < 24*hour; at += 10_000 {
		x := logUSD(p, code, at)
		d := x - prev
		sum, sq, prev = sum+d, sq+d*d, x
		n++
	}
	mean := sum / float64(n)
	return math.Sqrt(sq/float64(n) - mean*mean)
}

// Each currency moves by its own vol: a volatile one jitters more per step
// than a steady one.
func TestCurrenciesHaveOwnVol(t *testing.T) {
	p := path(5)
	steady, wild := stepSD(p, "TRY"), stepSD(p, "BRL")
	if !(wild > 3*steady) {
		t.Fatalf("BRL step sd %v, TRY %v: want BRL several times larger", wild, steady)
	}
}

// A depreciating currency trends down against USD; a currency with no
// fitted drift does not.
func TestCurrencyDriftTrends(t *testing.T) {
	p := path(5)
	if got := logUSD(p, "TRY", 20*hour) - logUSD(p, "TRY", 0); got > -0.5 {
		t.Fatalf("TRY log change over 20h %v, want a clear fall", got)
	}
	m := profile.DefaultMarkets()
	m.Currencies = []profile.Currency{{Code: "USD", Share: 0.5, USD: 1}, {Code: "EUR", Share: 0.5, USD: 1.1}}
	m.Exchanges = nil
	flat := New(seed(5), m)
	if got := math.Log(flat.USD(1, 20*hour) / 1.1); math.Abs(got) > 0.3 {
		t.Fatalf("a currency with no drift moved %v", got)
	}
}

// USD is the numeraire: it never moves.
func TestUSDIsConstant(t *testing.T) {
	p := path(5)
	for at := sched.Time(0); at < 3*hour; at += 7 * 60_000 {
		if got := logUSD(p, "USD", at); got != 0 {
			t.Fatalf("USD at %d = %v", at, got)
		}
	}
}
