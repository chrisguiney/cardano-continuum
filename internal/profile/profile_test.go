package profile

import (
	"bytes"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func open(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestParseMinimal(t *testing.T) {
	ps, err := Parse(bytes.NewReader(open(t, "testdata/minimal.yaml")))
	if err != nil {
		t.Fatal(err)
	}
	want := Profile{
		Name:   "casual-wallet",
		Kind:   Wallet,
		Agenda: "hodl",
		Traits: Traits{
			Competence:   Beta{A: 8, B: 2},
			Patience:     LogNormal{Mu: 1, Sigma: 0.5},
			LearningRate: Const{V: 0.05},
			Care:         Beta{A: 2, B: 2},
		},
		Budget: 1_000_000_000,
	}
	if len(ps) != 1 || !reflect.DeepEqual(ps[0], want) {
		t.Fatalf("got %#v, want %#v", ps, want)
	}
	if got := ps[0].Hazards.OnsetRate(); got != DefaultOnset {
		t.Errorf("default onset %v, want %v", got, DefaultOnset)
	}
	if got := ps[0].Rates.TailIndex(); got != DefaultTail {
		t.Errorf("default tail %v, want %v", got, DefaultTail)
	}
}

// TestPopulationDefaults pins the defaults to the values the population
// package used as constants, so existing runs and goldens are unchanged.
func TestPopulationDefaults(t *testing.T) {
	if DefaultOnset != 1.0/3600 || DefaultTail != 1.5 {
		t.Fatalf("onset %v tail %v", DefaultOnset, DefaultTail)
	}
}

func TestParseFull(t *testing.T) {
	ps, err := Parse(bytes.NewReader(open(t, "testdata/full.yaml")))
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 2 {
		t.Fatalf("got %d profiles", len(ps))
	}
	y := ps[0]
	if (y.Traits.Patience != Pareto{Xm: 50, Alpha: 1.3}) || y.Rates.Base[IntentSend] != 0.2 ||
		y.Rates.Hawkes != (Hawkes{Alpha: 0.3, Beta: 1.5}) || y.Hazards.Dormancy != (LogNormal{Mu: 2, Sigma: 0.8}) ||
		y.Weights[IntentDelegate] != 0.25 || y.Rates.TailIndex() != 2.5 || y.Hazards.OnsetRate() != 0.001 {
		t.Fatalf("unexpected yield-farmer: %#v", y)
	}
	g := ps[1]
	if g.Adversary == nil || g.Adversary.CostCeiling != 50_000_000 || len(g.Adversary.Attacks) != 2 ||
		g.Adversary.SuccessMetric != "mempool-occupancy" {
		t.Fatalf("unexpected adversary: %#v", g.Adversary)
	}
}

func TestParseRejects(t *testing.T) {
	tests := []struct {
		file string
		want string // substring naming the profile and field
	}{
		{"unknown-field", "colour"},
		{"negative-rate", `profile "p": rates.base.send`},
		{"beta-nonpositive", `profile "p": traits.competence`},
		{"unknown-intent", `profile "p": weights.teleport`},
		{"weights-sum", `profile "p": weights`},
		{"adversary-no-ceiling", `profile "p": adversary.cost_ceiling`},
		{"bad-dist-syntax", `profile "p": traits.competence`},
		{"wrong-version", "version"},
		{"unknown-kind", `profile "p": kind`},
		{"hawkes-unstable", `profile "p": rates.hawkes`},
		{"tail-too-light", `profile "p": rates.activity_tail`},
		{"onset-negative", `profile "p": hazards.dormancy_onset`},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			path := filepath.Join("testdata", "invalid", tt.file+".yaml")
			_, err := ParseNamed(path, bytes.NewReader(open(t, path)))
			if err == nil {
				t.Fatal("want error")
			}
			if !strings.Contains(err.Error(), tt.want) || !strings.Contains(err.Error(), path) {
				t.Fatalf("error %q lacks %q or the file name", err, tt.want)
			}
		})
	}
}

func TestParseRejectsOversizeAndEmpty(t *testing.T) {
	big := "version: 1\n# " + strings.Repeat("x", 2<<20) + "\nprofiles: []\n"
	if _, err := Parse(strings.NewReader(big)); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversize: got %v", err)
	}
	if _, err := Parse(strings.NewReader("")); err == nil {
		t.Fatal("empty input accepted")
	}
	if _, err := Parse(strings.NewReader("version: 1\nprofiles: []\n")); err == nil {
		t.Fatal("no profiles accepted")
	}
}

func TestParseDist(t *testing.T) {
	tests := []struct {
		in   string
		want Dist
	}{
		{"beta(8, 2)", Beta{8, 2}},
		{"beta(a=8,b=2)", Beta{8, 2}},
		{"lognormal(1.0, 0.5)", LogNormal{1, 0.5}},
		{"pareto(xm=50, alpha=1.3)", Pareto{50, 1.3}},
		{"pareto(50, 1.3)", Pareto{50, 1.3}},
		{"exp(2)", Exp{2}},
		{"const(-3)", Const{-3}},
	}
	for _, tt := range tests {
		got, err := parseDist(tt.in)
		if err != nil || got != tt.want {
			t.Errorf("parseDist(%q) = %v, %v; want %v", tt.in, got, err, tt.want)
		}
	}
	for _, in := range []string{
		"", "beta", "beta(1)", "beta(1,2,3)", "beta(a=1, c=2)", "beta(a=1, 2)", "beta(a=1, a=2)",
		"beta(1,2", "gauss(0,1)", "beta(NaN, 1)", "beta(inf, 1)", "lognormal(0, 0)", "pareto(0, 1)",
		"pareto(xm=1, alpha=-1)", "exp(0)", "beta(x, y)", "const(inf)",
	} {
		if d, err := parseDist(in); err == nil {
			t.Errorf("parseDist(%q) = %v, want error", in, d)
		}
	}
}

// seq is a deterministic Source: a splitmix64 stream, so the moment tests do
// not depend on any RNG package.
type seq struct{ s uint64 }

func (q *seq) Float64() float64 {
	q.s += 0x9e3779b97f4a7c15
	z := q.s
	z = (z ^ z>>30) * 0xbf58476d1ce4e5b9
	z = (z ^ z>>27) * 0x94d049bb133111eb
	z ^= z >> 31
	return float64(z>>11) / (1 << 53)
}

func TestDistMoments(t *testing.T) {
	tests := []struct {
		name       string
		d          Dist
		mean, vari float64
	}{
		{"beta(8,2)", Beta{8, 2}, 0.8, 8.0 * 2 / (100 * 11)},
		{"beta(0.5,0.5)", Beta{0.5, 0.5}, 0.5, 0.125},
		{"lognormal(1,0.5)", LogNormal{1, 0.5}, math.Exp(1 + 0.125), (math.Exp(0.25) - 1) * math.Exp(2+0.25)},
		{"pareto(2,5)", Pareto{2, 5}, 2.5, 4 * 5 / (16.0 * 3)},
		{"exp(4)", Exp{4}, 0.25, 1.0 / 16},
		{"const(3)", Const{3}, 3, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := &seq{s: 42}
			const n = 100_000
			var sum, sumsq float64
			for range n {
				x := tt.d.Sample(src)
				sum += x
				sumsq += x * x
			}
			mean := sum / n
			vari := sumsq/n - mean*mean
			if math.Abs(mean-tt.mean) > 0.03*math.Max(tt.mean, 0.1) {
				t.Errorf("mean %g, want %g", mean, tt.mean)
			}
			if math.Abs(vari-tt.vari) > 0.1*tt.vari+1e-12 {
				t.Errorf("variance %g, want %g", vari, tt.vari)
			}
		})
	}
}

func TestBetaStaysInUnitInterval(t *testing.T) {
	src := &seq{s: 7}
	for range 20_000 {
		if x := (Beta{0.05, 0.05}).Sample(src); x < 0 || x > 1 || math.IsNaN(x) {
			t.Fatalf("sample %g outside [0,1]", x)
		}
	}
}

type fixed struct{ v []float64 }

func (f *fixed) Float64() float64 {
	x := f.v[0]
	f.v = append(f.v[1:], x)
	return x
}

func TestTraitsSampleIsDeterministic(t *testing.T) {
	ps, err := Parse(bytes.NewReader(open(t, "testdata/minimal.yaml")))
	if err != nil {
		t.Fatal(err)
	}
	a := ps[0].Traits.Sample(&seq{s: 1})
	b := ps[0].Traits.Sample(&seq{s: 1})
	if a != b {
		t.Fatalf("same source gave %v and %v", a, b)
	}
	if a.LearningRate != 0.05 || a.Competence <= 0 || a.Competence >= 1 {
		t.Fatalf("unexpected sample %v", a)
	}
	if c := ps[0].Traits.Sample(&fixed{v: []float64{0.3, 0.6, 0.9}}); c.LearningRate != 0.05 {
		t.Fatalf("fixed source: %v", c)
	}
}

func TestScalePreservesShape(t *testing.T) {
	ps, err := Parse(bytes.NewReader(open(t, "testdata/full.yaml")))
	if err != nil {
		t.Fatal(err)
	}
	reg, err := NewRegistry(ps)
	if err != nil {
		t.Fatal(err)
	}
	scaled, err := reg.Scale(4)
	if err != nil {
		t.Fatal(err)
	}
	orig, _ := reg.Get("yield-farmer")
	got, ok := scaled.Get("yield-farmer")
	if !ok {
		t.Fatal("scaled registry lost the profile")
	}
	for k, v := range orig.Rates.Base {
		if got.Rates.Base[k] != 4*v {
			t.Errorf("rate %s = %g, want %g", k, got.Rates.Base[k], 4*v)
		}
	}
	if orig.Rates.Base[IntentSend] != 0.2 {
		t.Error("Scale mutated the receiver")
	}
	if !reflect.DeepEqual(orig.Weights, got.Weights) || orig.Traits != got.Traits ||
		orig.Rates.Hawkes != got.Rates.Hawkes || orig.Rates.Diurnal != got.Rates.Diurnal || orig.Hazards != got.Hazards {
		t.Error("Scale changed shape parameters")
	}
	for _, k := range []float64{0, -1, math.NaN(), math.Inf(1)} {
		if _, err := reg.Scale(k); err == nil {
			t.Errorf("Scale(%g) accepted", k)
		}
	}
}

func TestRegistry(t *testing.T) {
	ps, err := Parse(bytes.NewReader(open(t, "testdata/full.yaml")))
	if err != nil {
		t.Fatal(err)
	}
	reg, err := NewRegistry(ps)
	if err != nil {
		t.Fatal(err)
	}
	if p, ok := reg.Get("griefer"); !ok || p.Name != "griefer" {
		t.Fatalf("Get(griefer) = %v, %v", p, ok)
	}
	if _, ok := reg.Get("nobody"); ok {
		t.Fatal("Get(nobody) found something")
	}
	if got := reg.Names(); !reflect.DeepEqual(got, []string{"griefer", "yield-farmer"}) {
		t.Fatalf("Names = %v", got)
	}
}

func TestRegistryDuplicate(t *testing.T) {
	ps, err := Parse(bytes.NewReader(open(t, "testdata/minimal.yaml")))
	if err != nil {
		t.Fatal(err)
	}
	_, err = NewRegistry([]Profile{ps[0], ps[0]})
	if err == nil || !strings.Contains(err.Error(), "casual-wallet") {
		t.Fatalf("want duplicate error naming the profile, got %v", err)
	}
}

func FuzzParse(f *testing.F) {
	for _, p := range []string{"testdata/minimal.yaml", "testdata/full.yaml"} {
		b, err := os.ReadFile(p)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(b)
	}
	f.Fuzz(func(t *testing.T, in []byte) {
		ps, err := Parse(bytes.NewReader(in))
		if err != nil {
			return
		}
		out, err := Marshal(ps)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		again, err := Parse(bytes.NewReader(out))
		if err != nil {
			t.Fatalf("reparse of %q: %v", out, err)
		}
		if !reflect.DeepEqual(ps, again) {
			t.Fatalf("round trip changed value:\n%#v\n%#v", ps, again)
		}
	})
}
