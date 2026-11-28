package cohort

import (
	"slices"
	"strconv"

	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/wire"
	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
	"github.com/chrisguiney/cardano-continuum/internal/rng"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

type birth struct {
	prof profile.Profile
	at   sched.Time
	n    uint64
	h    sched.Handle
	// currency is the stream's currency in a market run, -1 otherwise.
	currency int
	// rate is the stream's base rate per second: the profile's birth
	// hazard times the currency's share.
	rate float64
	// mod is the market's birth factor for the currency; nil without one.
	mod sched.Modulation
}

// Births are the per-profile birth streams: each profile with a birth
// hazard adds actors at its own Poisson stream. In a market run a wallet
// profile has one stream per currency instead, at the profile's rate
// times the currency's share, thinned against the currency's birth factor
// (market.Path.Births): the factor is a function of the exogenous price
// path, so a stream's next birth is still its own draws alone.
type Births struct {
	list  []*birth // by profile name
	life  *Life
	seed  rng.Seed
	sched Scheduler
	emit  Emitter
}

// Add opens p's birth streams: one, or one per currency for a market
// wallet, in currency order.
func (bs *Births) Add(p profile.Profile) {
	m := bs.life.market
	if !m.On() || p.Kind != profile.Wallet {
		bs.list = append(bs.list, &birth{prof: p, currency: -1, rate: p.Hazards.Birth})
		return
	}
	for ci, c := range m.path.Params().Currencies {
		bs.list = append(bs.list, &birth{prof: p, currency: ci, rate: p.Hazards.Birth * c.Share, mod: m.path.Births(ci)})
	}
}

// Start schedules every stream's first birth after now.
func (bs *Births) Start(now sched.Time) {
	for _, b := range bs.list {
		bs.schedule(b, now)
	}
}

// Run spawns the member the birth event of stream idx brings, then
// schedules the stream's next.
func (bs *Births) Run(at sched.Time, idx int) {
	b := bs.list[idx]
	a := bs.life.Spawn(b.prof, at, b.currency)
	var currency string
	if tr := bs.life.r.Rec(a.ID).Trader; tr != nil {
		currency = bs.life.market.Code(tr.Primary)
	}
	sp := wire.Spawned{Profile: b.prof.Name, Currency: currency}
	if bs.life.people.On() {
		sp.Owner, sp.Purpose = uint64(a.Owner), string(a.Purpose)
	}
	bs.emit.Emit(at, wire.KindSpawned, a.ID, sp)
	bs.schedule(b, at)
}

func (bs *Births) schedule(b *birth, now sched.Time) {
	b.n++
	n := strconv.FormatUint(b.n, 10)
	if b.mod == nil {
		b.at = population.NextBirth(bs.seed.Derive("birth", b.prof.Name, n).Rand(), b.prof, now)
	} else {
		code := bs.life.market.Code(b.currency)
		b.at = population.NextArrival(bs.seed.Derive("birth", b.prof.Name, code, n).Rand(), b.rate, b.mod, now)
	}
	bs.push(b)
}

func (bs *Births) push(b *birth) {
	if b.at == population.Never {
		return
	}
	idx := slices.Index(bs.list, b)
	b.h = bs.sched.Birth(b.at, idx)
}

// Len is the number of streams.
func (bs *Births) Len() int { return len(bs.list) }

// Stream is stream i's profile name, next birth and draw count.
func (bs *Births) Stream(i int) (name string, at sched.Time, n uint64) {
	b := bs.list[i]
	return b.prof.Name, b.at, b.n
}

// Currency is stream i's currency code, empty outside a market.
func (bs *Births) Currency(i int) string {
	if c := bs.list[i].currency; c >= 0 {
		return bs.life.market.Code(c)
	}
	return ""
}

// Resume sets stream i to a snapshot's state and schedules its next birth.
func (bs *Births) Resume(i int, at sched.Time, n uint64) {
	b := bs.list[i]
	b.at, b.n = at, n
	bs.push(b)
}
