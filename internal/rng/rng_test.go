package rng_test

import (
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/chrisguiney/cardano-continuum/internal/rng"
)

var update = flag.Bool("update", false, "rewrite testdata golden files")

func seedOne() rng.Seed {
	var s rng.Seed
	s[31] = 1
	return s
}

func first(s rng.Seed, n int) []uint64 {
	r := s.Rand()
	out := make([]uint64, n)
	for i := range out {
		out[i] = r.Uint64()
	}
	return out
}

func checkGolden(t *testing.T, name, got string) {
	t.Helper()
	path := "testdata/" + name
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(want) != got {
		t.Fatalf("%s mismatch\nwant:\n%s\ngot:\n%s", name, want, got)
	}
}

func TestDeriveStable(t *testing.T) {
	d := seedOne().Derive("actor", "42", "activity")
	var b strings.Builder
	fmt.Fprintf(&b, "derive actor/42/activity: %s\n", hex.EncodeToString(d[:]))
	for _, v := range first(d, 4) {
		fmt.Fprintf(&b, "uint64: %016x\n", v)
	}
	checkGolden(t, "derive.golden", b.String())
}

func TestDeriveDeterministic(t *testing.T) {
	a := seedOne().Derive("a", "b")
	b := seedOne().Derive("a", "b")
	if a != b {
		t.Fatal("same inputs gave different seeds")
	}
}

func TestDerivePathUnambiguous(t *testing.T) {
	s := seedOne()
	paths := [][]string{
		{}, {""}, {"a"}, {"a", "b"}, {"ab"}, {"a", ""}, {"", "a"},
		{"a/b"}, {"b", "a"}, {"a", "b", "c"}, {"ab", "c"}, {"a", "bc"},
	}
	seen := map[rng.Seed]string{}
	for _, p := range paths {
		d := s.Derive(p...)
		key := fmt.Sprintf("%q", p)
		if prev, ok := seen[d]; ok {
			t.Fatalf("paths %s and %s collide", prev, key)
		}
		seen[d] = key
	}
}

func TestDeriveDiffersByRoot(t *testing.T) {
	var zero rng.Seed
	if zero.Derive("x") == seedOne().Derive("x") {
		t.Fatal("different roots gave the same child")
	}
	if seedOne().Derive("x") == seedOne() {
		t.Fatal("child equals parent")
	}
}

func TestDeriveIndependent(t *testing.T) {
	const streams, outputs = 1000, 8
	root := seedOne()
	seen := map[uint64]int{}
	var buckets [256]float64
	var total float64
	for i := range streams {
		for _, v := range first(root.Derive("actor", fmt.Sprint(i)), outputs) {
			if prev, ok := seen[v]; ok {
				t.Fatalf("streams %d and %d share output %x", prev, i, v)
			}
			seen[v] = i
			for k := range 8 {
				buckets[byte(v>>(8*k))]++
				total++
			}
		}
	}
	exp := total / 256
	var chi float64
	for _, c := range buckets {
		chi += (c - exp) * (c - exp) / exp
	}
	// df=255: mean 255, sd ~22.6. 400 is ~6.4 sd; inputs are fixed so this
	// is a regression tripwire, not a flaky statistical test.
	if chi > 400 {
		t.Fatalf("chi-square %.1f over pooled bytes", chi)
	}
}

func TestAddOrderIndependence(t *testing.T) {
	root := seedOne()
	want := first(root.Derive("actor", "7", "activity"), 16)
	for i := 1; i <= 6; i++ {
		_ = first(root.Derive("actor", fmt.Sprint(i), "activity"), 16)
	}
	got := first(root.Derive("actor", "7", "activity"), 16)
	for i := range want {
		if want[i] != got[i] {
			t.Fatalf("output %d changed after deriving siblings", i)
		}
	}
}

func TestRandIndependentInstances(t *testing.T) {
	s := seedOne()
	a, b := s.Rand(), s.Rand()
	a.Uint64()
	a.Uint64()
	if got, want := b.Uint64(), first(s, 1)[0]; got != want {
		t.Fatal("advancing one Rand moved another")
	}
}

func TestRandGoldenAcrossMethods(t *testing.T) {
	r := seedOne().Derive("golden").Rand()
	var b strings.Builder
	fmt.Fprintf(&b, "IntN(1000): %d %d %d\n", r.IntN(1000), r.IntN(1000), r.IntN(1000))
	fmt.Fprintf(&b, "Float64: %.17g\n", r.Float64())
	fmt.Fprintf(&b, "Perm(8): %v\n", r.Perm(8))
	checkGolden(t, "rand.golden", b.String())
}

func FuzzDeriveLabelIndependence(f *testing.F) {
	f.Add("a", "b")
	f.Add("actor", "1")
	f.Add("", "x")
	f.Fuzz(func(t *testing.T, a, b string) {
		root := seedOne()
		da, db := root.Derive(a), root.Derive(b)
		if (a == b) != (da == db) {
			t.Fatalf("label equality %v but seed equality %v", a == b, da == db)
		}
		// Splitting a path into nested derivations must not alias the flat path.
		if root.Derive(a, b) == root.Derive(a+b) && a != "" && b != "" {
			t.Fatalf("Derive(%q,%q) aliases Derive(%q)", a, b, a+b)
		}
	})
}
