package profile

import (
	"bytes"
	"math"
	"reflect"
	"sort"
	"strings"
	"testing"
)

const amountDoc = `version: 1
profiles:
  - name: payer
    kind: wallet
    agenda: hodl
    weight: 0.25
    traits:
      competence: beta(8, 2)
      patience: const(10)
      learning_rate: const(0)
      care: beta(2, 2)
    amount:
      body: lognormal(15, 1)
      tail: pareto(xm=20000000, alpha=1.5)
      tail_share: 0.1
`

func TestParseAmount(t *testing.T) {
	t.Parallel()
	ps, err := Parse(strings.NewReader(amountDoc))
	if err != nil {
		t.Fatal(err)
	}
	want := &Amount{Body: LogNormal{15, 1}, Tail: &Pareto{2e7, 1.5}, TailShare: 0.1}
	if !reflect.DeepEqual(ps[0].Amount, want) || ps[0].Weight != 0.25 {
		t.Fatalf("got %#v weight %v", ps[0].Amount, ps[0].Weight)
	}
	raw, err := Marshal(ps)
	if err != nil {
		t.Fatal(err)
	}
	again, err := Parse(bytes.NewReader(raw))
	if err != nil || !reflect.DeepEqual(again, ps) {
		t.Fatalf("round trip: %v\n%s", err, raw)
	}
}

// A profile without the field has no amount and no weight, and marshals
// without either key, so its hash and every golden stay as they were.
func TestOldProfilesHaveNoAmount(t *testing.T) {
	t.Parallel()
	ps, err := Parse(bytes.NewReader(open(t, "testdata/minimal.yaml")))
	if err != nil {
		t.Fatal(err)
	}
	if ps[0].Amount != nil || ps[0].Weight != 0 {
		t.Fatalf("got %#v", ps[0])
	}
	raw, err := Marshal(ps)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("amount")) || bytes.Contains(raw, []byte("weight")) {
		t.Fatalf("marshal leaks new keys:\n%s", raw)
	}
}

func TestParseAmountRejects(t *testing.T) {
	t.Parallel()
	head := strings.Split(amountDoc, "    amount:")[0]
	tests := map[string]string{
		"no body":            "    amount:\n      tail_share: 0.1\n",
		"body not lognormal": "    amount:\n      body: const(5)\n",
		"tail no share":      "    amount:\n      body: lognormal(15, 1)\n      tail: pareto(xm=1, alpha=2)\n",
		"share no tail":      "    amount:\n      body: lognormal(15, 1)\n      tail_share: 0.1\n",
		"share over one":     "    amount:\n      body: lognormal(15, 1)\n      tail: pareto(xm=1, alpha=2)\n      tail_share: 1.5\n",
		"tail not pareto":    "    amount:\n      body: lognormal(15, 1)\n      tail: lognormal(1, 1)\n      tail_share: 0.1\n",
	}
	for name, doc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := Parse(strings.NewReader(head + doc))
			if err == nil || !strings.Contains(err.Error(), "amount") {
				t.Fatalf("want an amount error, got %v", err)
			}
		})
	}
	_, err := Parse(strings.NewReader(strings.Replace(amountDoc, "weight: 0.25", "weight: -1", 1)))
	if err == nil || !strings.Contains(err.Error(), "weight") {
		t.Fatalf("negative weight: %v", err)
	}
}

// With a fixed source the sample's quantiles follow the body below the tail
// scale and the tail above it.
func TestAmountSampleFollowsFit(t *testing.T) {
	t.Parallel()
	a := Amount{Body: LogNormal{15, 1}, Tail: &Pareto{2e7, 1.5}, TailShare: 0.1}
	src := &seq{s: 42}
	const n = 200_000
	xs := make([]float64, n)
	above := 0
	for i := range xs {
		xs[i] = a.Sample(src)
		if xs[i] >= 2e7 {
			above++
		}
	}
	// The body is truncated at the tail scale, so everything above it
	// comes from the tail: its share is the tail share.
	if got := float64(above) / n; math.Abs(got-0.1) > 0.005 {
		t.Errorf("tail share %v, want 0.1", got)
	}
	sort.Float64s(xs)
	// Median of the body conditioned below xm: exp(mu + sigma*z) with
	// z = Phi^-1(0.5 * Phi((ln xm - mu)/sigma)) over the 90% body mass.
	phi := func(z float64) float64 { return 0.5 * math.Erfc(-z/math.Sqrt2) }
	zmax := (math.Log(2e7) - 15) / 1
	const q = 0.5 // the overall median lies in the body
	lo, hi := -8.0, 8.0
	for range 100 {
		mid := (lo + hi) / 2
		if phi(mid)/phi(zmax)*0.9 < q {
			lo = mid
		} else {
			hi = mid
		}
	}
	want := math.Exp(15 + lo)
	if got := xs[n/2]; math.Abs(math.Log(got/want)) > 0.03 {
		t.Errorf("median %v, want %v", got, want)
	}
	// Pareto tail: P(X > 2*xm | tail) = 2^-alpha.
	var over2 int
	for _, x := range xs {
		if x > 4e7 {
			over2++
		}
	}
	if got, want := float64(over2)/float64(above), math.Pow(2, -1.5); math.Abs(got-want) > 0.01 {
		t.Errorf("tail ratio %v, want %v", got, want)
	}
}

func TestAmountBodyOnly(t *testing.T) {
	t.Parallel()
	a := Amount{Body: LogNormal{15, 1}}
	src := &seq{s: 1}
	var s float64
	for range 50_000 {
		s += math.Log(a.Sample(src))
	}
	if m := s / 50_000; math.Abs(m-15) > 0.03 {
		t.Fatalf("mean log %v, want 15", m)
	}
}

func TestParseAmountBodyOnly(t *testing.T) {
	t.Parallel()
	doc := strings.Split(amountDoc, "    amount:")[0] + "    amount:\n      body: lognormal(15, 1)\n"
	ps, err := Parse(strings.NewReader(doc))
	if err != nil {
		t.Fatal(err)
	}
	if want := (&Amount{Body: LogNormal{15, 1}}); !reflect.DeepEqual(ps[0].Amount, want) {
		t.Fatalf("got %#v", ps[0].Amount)
	}
}
