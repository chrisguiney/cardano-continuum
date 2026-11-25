package upgrade

import (
	"fmt"
	"math"
	"slices"
	"strings"
	"testing"

	"github.com/chrisguiney/cardano-continuum/internal/rng"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

var curve = Curve{Start: 1000, End: 11000, Steepness: 6}

func TestCurveBounds(t *testing.T) {
	if got := curve.Fraction(curve.Start); got != 0 {
		t.Errorf("Fraction(Start) = %v, want 0", got)
	}
	if got := curve.Fraction(curve.End); got != 1 {
		t.Errorf("Fraction(End) = %v, want 1", got)
	}
	if got := curve.Fraction(curve.Start - 500); got != 0 {
		t.Errorf("before Start = %v, want 0", got)
	}
	if got := curve.Fraction(curve.End + 500); got != 1 {
		t.Errorf("after End = %v, want 1", got)
	}
	if got := curve.Fraction(6000); math.Abs(got-0.5) > 1e-12 {
		t.Errorf("Fraction(midpoint) = %v, want 0.5", got)
	}
}

func TestCurveMonotone(t *testing.T) {
	for _, s := range []float64{0.5, 1, 6, 12} {
		c := Curve{Start: 0, End: 10000, Steepness: s}
		prev := -1.0
		for tm := sched.Time(-100); tm <= 10100; tm += 50 {
			f := c.Fraction(tm)
			if f < prev || f < 0 || f > 1 {
				t.Fatalf("steepness %v: Fraction(%d) = %v after %v", s, tm, f, prev)
			}
			prev = f
		}
	}
}

func TestCurveReachesFractionByTime(t *testing.T) {
	for _, p := range []float64{0.1, 0.3, 0.5, 0.9} {
		at := curve.When(p)
		if f := curve.Fraction(at); math.Abs(f-p) > 1e-3 {
			t.Errorf("Fraction(When(%v)) = %v", p, f)
		}
		if curve.Fraction(at-1000) >= p {
			t.Errorf("%v reached before When(%v)", p, p)
		}
	}
	if curve.When(0) != curve.Start || curve.When(1) != curve.End {
		t.Errorf("When(0), When(1) = %d, %d", curve.When(0), curve.When(1))
	}
}

func pools(n int) []Pool {
	out := make([]Pool, n)
	for i := range out {
		out[i] = Pool{ID: fmt.Sprintf("pool%03d", i), Eagerness: 1}
	}
	return out
}

func TestShareAdoptedByTime(t *testing.T) {
	sched := Plan(curve, "dingo:v2", pools(2000), rng.Seed{1}.Rand())
	if len(sched) != 2000 {
		t.Fatalf("%d intents, want 2000", len(sched))
	}
	for _, p := range []float64{0.25, 0.5, 0.75} {
		at := curve.When(p)
		n := 0
		for _, in := range sched {
			if in.At <= at {
				n++
			}
		}
		if got := float64(n) / 2000; math.Abs(got-p) > 0.04 {
			t.Errorf("%.0f%% expected by %d, %.1f%% adopted", p*100, at, got*100)
		}
	}
}

func TestEagernessAdoptsEarlier(t *testing.T) {
	for _, u := range []float64{0.1, 0.5, 0.9} {
		if !(AdoptAt(curve, u, 3) < AdoptAt(curve, u, 1) && AdoptAt(curve, u, 1) < AdoptAt(curve, u, 0.3)) {
			t.Errorf("u=%v: eagerness does not order adoption", u)
		}
	}
	for _, at := range []sched.Time{AdoptAt(curve, 0.5, 0.01), AdoptAt(curve, 0.999, 100)} {
		if at < curve.Start || at > curve.End {
			t.Errorf("adoption at %d outside the wave", at)
		}
	}
}

func TestPlanSameSeedSameSchedule(t *testing.T) {
	a := Plan(curve, "dingo:v2", pools(50), rng.Seed{7}.Rand())
	b := Plan(curve, "dingo:v2", pools(50), rng.Seed{7}.Rand())
	if !slices.Equal(a, b) {
		t.Fatal("same seed, different schedule")
	}
	c := Plan(curve, "dingo:v2", pools(50), rng.Seed{8}.Rand())
	if slices.Equal(a, c) {
		t.Fatal("different seed, same schedule")
	}
}

func TestPlanIgnoresInputOrder(t *testing.T) {
	ps := pools(30)
	want := Plan(curve, "r", ps, rng.Seed{3}.Rand())
	slices.Reverse(ps)
	if got := Plan(curve, "r", ps, rng.Seed{3}.Rand()); !slices.Equal(got, want) {
		t.Fatal("input order changed the schedule")
	}
}

func TestPlanOrdered(t *testing.T) {
	s := Plan(curve, "r", pools(100), rng.Seed{5}.Rand())
	if !slices.IsSortedFunc(s, func(a, b Intent) int {
		if a.At != b.At {
			return int(a.At - b.At)
		}
		return strings.Compare(a.Pool, b.Pool)
	}) {
		t.Fatal("intents not ordered by time then pool")
	}
	if got := s[0].String(); got != fmt.Sprintf("ImageChange(%s, r)", s[0].Pool) {
		t.Errorf("String() = %q", got)
	}
}

func TestValidate(t *testing.T) {
	for _, c := range []Curve{{Start: 5, End: 5, Steepness: 1}, {Start: 9, End: 5, Steepness: 1}, {Start: 0, End: 5, Steepness: 0}, {Start: 0, End: 5, Steepness: math.NaN()}} {
		if c.Validate() == nil {
			t.Errorf("%+v accepted", c)
		}
	}
	if err := curve.Validate(); err != nil {
		t.Error(err)
	}
}
