package sched_test

import (
	"context"
	"errors"
	"math"
	"math/rand/v2"
	"sort"
	"testing"
	"testing/quick"
	"time"

	"github.com/chrisguiney/cardano-continuum/internal/rng"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

func newRand(label string) *rand.Rand {
	return rng.Seed{1}.Derive(label).Rand()
}

func TestQueueOrder(t *testing.T) {
	var q sched.Queue
	q.Push(10, "b1")
	q.Push(5, "a")
	q.Push(10, "b2")
	q.Push(10, "b3")
	q.Push(1, "z")
	var got []string
	for {
		_, ev, ok := q.Pop()
		if !ok {
			break
		}
		got = append(got, ev.(string))
	}
	want := []string{"z", "a", "b1", "b2", "b3"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
}

func TestQueueCancel(t *testing.T) {
	var q sched.Queue
	h1 := q.Push(1, "a")
	h2 := q.Push(2, "b")
	q.Push(3, "c")
	q.Cancel(h1)
	q.Cancel(h1)
	if at, ok := q.Peek(); !ok || at != 2 {
		t.Fatalf("peek = %v %v", at, ok)
	}
	_, ev, _ := q.Pop()
	if ev != "b" {
		t.Fatalf("got %v", ev)
	}
	q.Cancel(h2) // already popped: no-op, must not disturb c
	if q.Len() != 1 {
		t.Fatalf("len = %d", q.Len())
	}
	if _, ev, _ := q.Pop(); ev != "c" {
		t.Fatalf("got %v", ev)
	}
	if _, _, ok := q.Pop(); ok {
		t.Fatal("empty queue popped")
	}
	if _, ok := q.Peek(); ok {
		t.Fatal("empty queue peeked")
	}
}

func TestQueueMatchesStableSort(t *testing.T) {
	type op struct {
		At     uint8
		Cancel uint8
		Do     bool
	}
	f := func(ops []op) bool {
		var q sched.Queue
		type rec struct {
			at   sched.Time
			id   int
			dead bool
		}
		var recs []*rec
		var hs []sched.Handle
		for i, o := range ops {
			if o.Do && len(hs) > 0 {
				j := int(o.Cancel) % len(hs)
				q.Cancel(hs[j])
				recs[j].dead = true
				continue
			}
			recs = append(recs, &rec{at: sched.Time(o.At % 8), id: i})
			hs = append(hs, q.Push(sched.Time(o.At%8), i))
		}
		var want []*rec
		for _, r := range recs {
			if !r.dead {
				want = append(want, r)
			}
		}
		sort.SliceStable(want, func(a, b int) bool { return want[a].at < want[b].at })
		for _, w := range want {
			at, ev, ok := q.Pop()
			if !ok || at != w.at || ev.(int) != w.id {
				return false
			}
		}
		_, _, ok := q.Pop()
		return !ok
	}
	if err := quick.Check(f, &quick.Config{MaxCount: 500}); err != nil {
		t.Fatal(err)
	}
}

func TestPoissonRate(t *testing.T) {
	r := newRand("poisson")
	var now sched.Time
	const n = 50000
	for range n {
		now = sched.NextArrival(r, 2, nil, now)
	}
	mean := float64(now) / n
	if math.Abs(mean-500)/500 > 0.02 {
		t.Fatalf("mean gap %.1fms, want 500ms +-2%%", mean)
	}
}

func TestArrivalAlwaysAdvances(t *testing.T) {
	r := newRand("adv")
	now := sched.Time(0)
	for range 1000 {
		next := sched.NextArrival(r, 1e6, nil, now)
		if next <= now {
			t.Fatalf("next %d not after %d", next, now)
		}
		now = next
	}
}

func TestDiurnalThinning(t *testing.T) {
	// 1 hour on at factor 1, 1 hour off at floor 0.1.
	const hour = sched.Time(3600_000)
	mod := sched.SquareWave{Period: 2 * hour, OnFraction: 0.5, Floor: 0.1}
	r := newRand("diurnal")
	var on, off int
	var now sched.Time
	horizon := 200 * hour
	for {
		now = sched.NextArrival(r, 1.0/60, mod, now) // 1 per minute when on
		if now >= horizon {
			break
		}
		if now%(2*hour) < hour {
			on++
		} else {
			off++
		}
	}
	ratio := float64(off) / float64(on)
	if math.Abs(ratio-0.1) > 0.01 {
		t.Fatalf("off/on = %.4f, want 0.1 (on=%d off=%d)", ratio, on, off)
	}
}

func TestModulationBounds(t *testing.T) {
	d := sched.Daily{Amplitude: 0.5}
	w := sched.Weekly{Weekend: 0.4}
	for ms := sched.Time(0); ms < 8*24*3600_000; ms += 600_000 {
		for _, m := range []sched.Modulation{d, w, sched.Product{d, w}} {
			f := m.Factor(ms)
			if f < 0 || f > m.Max()+1e-12 {
				t.Fatalf("factor %v outside [0,%v] at %d", f, m.Max(), ms)
			}
		}
	}
}

func TestHawkesRejectsExplosive(t *testing.T) {
	for _, c := range [][3]float64{{1, 1, 1}, {1, 2, 1}, {0, 1, 2}, {1, -1, 2}, {1, 1, 0}} {
		if _, err := sched.NewHawkes(c[0], c[1], c[2]); err == nil {
			t.Fatalf("NewHawkes%v accepted", c)
		}
	}
	if _, err := sched.NewHawkes(1, 0.5, 1); err != nil {
		t.Fatal(err)
	}
}

func TestHawkesIntensityDecays(t *testing.T) {
	h, _ := sched.NewHawkes(1, 0.5, 1)
	if got := h.Intensity(0); got != 1 {
		t.Fatalf("base = %v", got)
	}
	h.Excite(0)
	if got := h.Intensity(0); math.Abs(got-1.5) > 1e-12 {
		t.Fatalf("after excite = %v", got)
	}
	want := 1 + 0.5*math.Exp(-1)
	if got := h.Intensity(1000); math.Abs(got-want) > 1e-12 {
		t.Fatalf("after 1s = %v want %v", got, want)
	}
}

func TestHawkesBranching(t *testing.T) {
	h, _ := sched.NewHawkes(1, 0.5, 1) // branching 0.5, long-run rate 2/s
	r := newRand("hawkes")
	var now sched.Time
	const n = 100000
	for range n {
		now = h.Next(r, now)
	}
	rate := n / (float64(now) / 1000)
	if math.Abs(rate-2)/2 > 0.05 {
		t.Fatalf("rate %.3f/s, want 2 +-5%%", rate)
	}
}

func TestVirtualClock(t *testing.T) {
	c := sched.NewVirtual(0)
	if err := c.Wait(context.Background(), 5000); err != nil {
		t.Fatal(err)
	}
	if c.Now() != 5000 {
		t.Fatalf("now = %d", c.Now())
	}
	if err := c.Wait(context.Background(), 100); err != nil || c.Now() != 5000 {
		t.Fatalf("time went backwards: %d %v", c.Now(), err)
	}
}

func TestWallClockCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c := sched.NewWall(time.Now())
	if err := c.Wait(ctx, 1<<40); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

func TestWallClockPastTargetReturns(t *testing.T) {
	c := sched.NewWall(time.Now().Add(-time.Hour))
	if err := c.Wait(context.Background(), 1000); err != nil {
		t.Fatal(err)
	}
	if c.Now() < 3600_000 {
		t.Fatalf("now = %d", c.Now())
	}
}

func TestSlotArithmetic(t *testing.T) {
	// 1s slots, 100-slot epochs.
	g, err := sched.NewSlots(1000, 100)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		t     sched.Time
		slot  uint64
		epoch uint64
		next  sched.Time
	}{
		{0, 0, 0, 100_000},
		{999, 0, 0, 100_000},
		{1000, 1, 0, 100_000},
		{99_999, 99, 0, 100_000},
		{100_000, 100, 1, 200_000},
		{250_500, 250, 2, 300_000},
	}
	for _, c := range cases {
		if s := g.SlotAt(c.t); s != c.slot {
			t.Errorf("SlotAt(%d)=%d want %d", c.t, s, c.slot)
		}
		if e := g.EpochAt(c.t); e != c.epoch {
			t.Errorf("EpochAt(%d)=%d want %d", c.t, e, c.epoch)
		}
		if n := g.NextEpochStart(c.t); n != c.next {
			t.Errorf("NextEpochStart(%d)=%d want %d", c.t, n, c.next)
		}
	}
	if g.SlotStart(101) != 101_000 || g.EpochStart(3) != 300_000 || g.EpochOfSlot(199) != 1 {
		t.Fatal("start helpers wrong")
	}
	if _, err := sched.NewSlots(0, 10); err == nil {
		t.Fatal("zero slot length accepted")
	}
	if _, err := sched.NewSlots(1000, 0); err == nil {
		t.Fatal("zero epoch length accepted")
	}
}

func TestQueuePopAllocs(t *testing.T) {
	var q sched.Queue
	for i := range 1024 {
		q.Push(sched.Time(i), i)
	}
	allocs := testing.AllocsPerRun(1000, func() { q.Pop() })
	if allocs != 0 {
		t.Fatalf("allocs per pop = %v", allocs)
	}
}

func BenchmarkQueuePushPop(b *testing.B) {
	var q sched.Queue
	r := newRand("bench")
	for i := 0; i < 1024; i++ {
		q.Push(sched.Time(r.IntN(1<<20)), i)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		at, ev, _ := q.Pop()
		q.Push(at+sched.Time(r.IntN(1<<10)), ev)
	}
}

// excitedRun draws n arrivals of the modulated Hawkes process, exciting at
// each, and returns their times.
func excitedRun(seed uint64, n int, mu, alpha, beta float64, mod sched.Modulation) []sched.Time {
	r := rand.New(rand.NewPCG(seed, seed))
	var x sched.Excitation
	var out []sched.Time
	now := sched.Time(0)
	for range n {
		now, _ = sched.NextExcited(r, mu, beta, x, mod, now)
		x = x.Excite(now, alpha, beta)
		out = append(out, now)
	}
	return out
}

// Without excitation the process is the modulated Poisson process; with
// branching ratio alpha/beta its long-run rate is mu/(1-alpha/beta), and
// its gaps are overdispersed (coefficient of variation above 1), which is
// what bursts are.
func TestNextExcitedRateAndBursts(t *testing.T) {
	const n = 20000
	cv := func(ts []sched.Time) (rate, cv float64) {
		var sum, sq float64
		prev := sched.Time(0)
		for _, x := range ts {
			g := float64(x - prev)
			sum, sq, prev = sum+g, sq+g*g, x
		}
		m := sum / float64(len(ts))
		return 1000 / m, math.Sqrt(sq/float64(len(ts))-m*m) / m
	}
	r0, c0 := cv(excitedRun(1, n, 0.5, 0, 1, nil))
	if math.Abs(r0-0.5) > 0.02 || math.Abs(c0-1) > 0.05 {
		t.Fatalf("unexcited: rate %.3f cv %.2f, want 0.5 and 1", r0, c0)
	}
	r1, c1 := cv(excitedRun(2, n, 0.25, 0.5, 1, nil))
	if math.Abs(r1-0.5) > 0.03 || c1 < 1.2 {
		t.Fatalf("excited: rate %.3f cv %.2f, want 0.5 and bursty", r1, c1)
	}
}

func TestNextExcitedFollowsModulation(t *testing.T) {
	ts := excitedRun(3, 20000, 0.01, 0.005, 0.02, sched.Daily{Amplitude: 0.9})
	var peak, trough int
	for _, x := range ts {
		if (x % (24 * 3600_000)) < 12*3600_000 {
			peak++
		} else {
			trough++
		}
	}
	if float64(peak) < 2*float64(trough) {
		t.Fatalf("%d arrivals in the peak half-day, %d in the trough", peak, trough)
	}
}

// The excitation decays at beta and adds alpha per event.
func TestExcitationDecays(t *testing.T) {
	x := sched.Excitation{}.Excite(1000, 0.4, 2)
	if got := x.At(1000, 2); math.Abs(got-0.4) > 1e-12 {
		t.Fatalf("at the event %v, want 0.4", got)
	}
	if got, want := x.At(2000, 2), 0.4*math.Exp(-2); math.Abs(got-want) > 1e-12 {
		t.Fatalf("a second later %v, want %v", got, want)
	}
	if got := x.Excite(2000, 0.4, 2).At(2000, 2); math.Abs(got-(0.4*math.Exp(-2)+0.4)) > 1e-12 {
		t.Fatalf("second excitation %v", got)
	}
}

// The share of offspring among arrivals is the branching ratio
// alpha/beta: each event begets that many on average.
func TestNextExcitedOffspringShare(t *testing.T) {
	r := rand.New(rand.NewPCG(5, 5))
	var x sched.Excitation
	now, kids := sched.Time(0), 0
	const n = 20000
	for range n {
		var off bool
		now, off = sched.NextExcited(r, 0.1, 1, x, nil, now)
		x = x.Excite(now, 0.6, 1)
		if off {
			kids++
		}
	}
	if got := float64(kids) / n; math.Abs(got-0.6) > 0.03 {
		t.Fatalf("offspring share %.3f, want 0.6", got)
	}
}
