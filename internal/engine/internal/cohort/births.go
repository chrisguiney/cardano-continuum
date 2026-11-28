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
}

// Births are the per-profile birth streams: each profile with a birth
// hazard adds actors at its own Poisson stream.
type Births struct {
	list  []*birth // by profile name
	life  *Life
	seed  rng.Seed
	sched Scheduler
	emit  Emitter
}

// Add opens a birth stream for p.
func (bs *Births) Add(p profile.Profile) { bs.list = append(bs.list, &birth{prof: p}) }

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
	a := bs.life.Spawn(b.prof, at, -1)
	var currency string
	if tr := bs.life.r.Rec(a.ID).Trader; tr != nil {
		currency = bs.life.market.Code(tr.Primary)
	}
	bs.emit.Emit(at, wire.KindSpawned, a.ID, wire.Spawned{Profile: b.prof.Name, Currency: currency})
	bs.schedule(b, at)
}

func (bs *Births) schedule(b *birth, now sched.Time) {
	b.n++
	r := bs.seed.Derive("birth", b.prof.Name, strconv.FormatUint(b.n, 10)).Rand()
	b.at = population.NextBirth(r, b.prof, now)
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

// Resume sets stream i to a snapshot's state and schedules its next birth.
func (bs *Births) Resume(i int, at sched.Time, n uint64) {
	b := bs.list[i]
	b.at, b.n = at, n
	bs.push(b)
}
