package mistake

import (
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/chrisguiney/cardano-continuum/internal/profile"
	"github.com/chrisguiney/cardano-continuum/internal/rng"
	"github.com/chrisguiney/cardano-continuum/internal/submit"
)

func stream(n byte) *rand.Rand { return rng.Seed{n}.Derive("mistake").Rand() }

func TestSampleDeterministic(t *testing.T) {
	tr := profile.SampledTraits{Competence: 0.4, Care: 0.3}
	all := Of(Kinds()...)
	a, b := stream(1), stream(1)
	var got []KindSet
	for range 1000 {
		x, y := Sample(tr, all, a), Sample(tr, all, b)
		if x != y {
			t.Fatalf("same stream gave %v and %v", x, y)
		}
		got = append(got, x)
	}
	c := stream(2)
	var other []KindSet
	for range 1000 {
		other = append(other, Sample(tr, all, c))
	}
	if slices.Equal(got, other) {
		t.Fatal("different streams gave the same draws")
	}
}

func TestSampleRateAndChoice(t *testing.T) {
	all := Of(Kinds()...)
	only := Of(FeeTooSmall, MissingSigner)
	const n = 20000
	for _, tr := range []profile.SampledTraits{
		{Competence: 1, Care: 0}, {Competence: 0, Care: 1},
		{Competence: 0.5, Care: 0.5}, {Competence: 0, Care: 0},
	} {
		r := stream(3)
		var made int
		for range n {
			ks := Sample(tr, only, r)
			if ks == 0 {
				continue
			}
			made++
			if len(ks.Kinds()) != 1 || ks.Without(only) != 0 {
				t.Fatalf("%+v: sampled %v, want one of %v", tr, ks, only)
			}
		}
		want := Rate(tr)
		if got := float64(made) / n; got < want-0.02 || got > want+0.02 {
			t.Errorf("%+v: rate %.3f, want %.3f", tr, got, want)
		}
	}
	if Sample(profile.SampledTraits{}, 0, stream(4)) != 0 {
		t.Fatal("a mistake sampled with none applicable")
	}
	seen := KindSet(0)
	r := stream(5)
	for range 2000 {
		seen |= Sample(profile.SampledTraits{}, all, r)
	}
	if seen != all {
		t.Fatalf("competence 0 never chose %v", all.Without(seen))
	}
}

func TestDiagnoseAccuracyRises(t *testing.T) {
	fs := []submit.Failure{{Class: submit.FeeTooSmall}, {Class: submit.MissingVKeyWitnesses}}
	const n = 20000
	prev := -1.0
	for _, c := range []float64{0, 0.2, 0.4, 0.6, 0.8, 1} {
		d := Diagnoser{Competence: c}
		r := stream(6)
		var right int
		for range n {
			got := d.Diagnose(r, fs)
			if slices.ContainsFunc(fs, func(f submit.Failure) bool { return f.Class == got }) {
				right++
			}
		}
		acc := float64(right) / n
		if acc <= prev {
			t.Fatalf("competence %.1f: accuracy %.3f not above %.3f", c, acc, prev)
		}
		prev = acc
	}
	if prev != 1 {
		t.Fatalf("competence 1: accuracy %.3f, want 1", prev)
	}
	var _ submit.Diagnoser = Diagnoser{}
	if got := (Diagnoser{Competence: 1}).Diagnose(stream(7), nil); got != submit.Unknown {
		t.Fatalf("no failures diagnosed as %s", got)
	}
}

func TestDiagnoseDeterministic(t *testing.T) {
	fs := []submit.Failure{{Class: submit.BadInputs}}
	d := Diagnoser{Competence: 0.3}
	a, b := stream(8), stream(8)
	for range 500 {
		if x, y := d.Diagnose(a, fs), d.Diagnose(b, fs); x != y {
			t.Fatalf("same stream diagnosed %s and %s", x, y)
		}
	}
}
