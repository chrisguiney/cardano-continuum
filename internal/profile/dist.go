package profile

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Source supplies uniform randomness to samplers. Float64 returns a value in
// [0, 1). Samplers draw only through this method, so any generator that has
// it, such as *math/rand/v2.Rand, can drive them.
type Source interface {
	Float64() float64
}

// Dist is a parameterised distribution over float64.
type Dist interface {
	// Sample draws one value using src.
	Sample(src Source) float64
	// String returns the canonical text form accepted by the profile parser.
	String() string
}

// Beta is the beta distribution on [0, 1] with shape parameters A, B > 0.
type Beta struct{ A, B float64 }

// LogNormal is exp(N(Mu, Sigma^2)) with Sigma > 0.
type LogNormal struct{ Mu, Sigma float64 }

// Pareto is the type I Pareto distribution with scale Xm > 0 and shape Alpha > 0.
type Pareto struct{ Xm, Alpha float64 }

// Exp is the exponential distribution with Rate > 0.
type Exp struct{ Rate float64 }

// Const always yields V.
type Const struct{ V float64 }

// open01 returns a uniform value in (0, 1], so its logarithm is finite.
func open01(src Source) float64 { return 1 - src.Float64() }

// normal draws a standard normal by the Box-Muller transform. It discards the
// second variate, which keeps the number of draws per sample independent of
// call history.
func normal(src Source) float64 {
	u1, u2 := open01(src), src.Float64()
	return math.Sqrt(-2*math.Log(u1)) * math.Cos(2*math.Pi*u2)
}

// gamma draws Gamma(shape, 1) by Marsaglia-Tsang. For shape < 1 it samples
// shape+1 and rescales by u^(1/shape).
func gamma(src Source, shape float64) float64 {
	if shape < 1 {
		return gamma(src, shape+1) * math.Pow(open01(src), 1/shape)
	}
	d := shape - 1.0/3
	c := 1 / math.Sqrt(9*d)
	for {
		x := normal(src)
		v := 1 + c*x
		if v <= 0 {
			continue
		}
		v = v * v * v
		u := open01(src)
		if math.Log(u) < 0.5*x*x+d-d*v+d*math.Log(v) {
			return d * v
		}
	}
}

// Sample implements Dist.
func (b Beta) Sample(src Source) float64 {
	x, y := gamma(src, b.A), gamma(src, b.B)
	if s := x + y; s > 0 {
		return x / s
	}
	// Both gammas underflowed, which happens only for very small shapes;
	// the mean is the least surprising stand-in.
	return b.A / (b.A + b.B)
}

// Sample implements Dist.
func (l LogNormal) Sample(src Source) float64 { return math.Exp(l.Mu + l.Sigma*normal(src)) }

// Sample implements Dist.
func (p Pareto) Sample(src Source) float64 { return p.Xm / math.Pow(open01(src), 1/p.Alpha) }

// Sample implements Dist.
func (e Exp) Sample(src Source) float64 { return -math.Log(open01(src)) / e.Rate }

// Sample implements Dist.
func (c Const) Sample(Source) float64 { return c.V }

func ff(v float64) string { return strconv.FormatFloat(v, 'g', -1, 64) }

func (b Beta) String() string      { return "beta(" + ff(b.A) + ", " + ff(b.B) + ")" }
func (l LogNormal) String() string { return "lognormal(" + ff(l.Mu) + ", " + ff(l.Sigma) + ")" }
func (p Pareto) String() string    { return "pareto(xm=" + ff(p.Xm) + ", alpha=" + ff(p.Alpha) + ")" }
func (e Exp) String() string       { return "exp(" + ff(e.Rate) + ")" }
func (c Const) String() string     { return "const(" + ff(c.V) + ")" }

// distSpec describes the textual form of one distribution: its parameter
// names, in positional order, and how to build and range-check it.
type distSpec struct {
	params []string
	build  func(v []float64) (Dist, error)
}

func positive(name string, v float64) error {
	if !(v > 0) {
		return fmt.Errorf("%s must be > 0, got %s", name, ff(v))
	}
	return nil
}

var distSpecs = map[string]distSpec{
	"beta": {[]string{"a", "b"}, func(v []float64) (Dist, error) {
		if err := positive("a", v[0]); err != nil {
			return nil, err
		}
		if err := positive("b", v[1]); err != nil {
			return nil, err
		}
		return Beta{v[0], v[1]}, nil
	}},
	"lognormal": {[]string{"mu", "sigma"}, func(v []float64) (Dist, error) {
		if err := positive("sigma", v[1]); err != nil {
			return nil, err
		}
		return LogNormal{v[0], v[1]}, nil
	}},
	"pareto": {[]string{"xm", "alpha"}, func(v []float64) (Dist, error) {
		if err := positive("xm", v[0]); err != nil {
			return nil, err
		}
		if err := positive("alpha", v[1]); err != nil {
			return nil, err
		}
		return Pareto{v[0], v[1]}, nil
	}},
	"exp": {[]string{"rate"}, func(v []float64) (Dist, error) {
		if err := positive("rate", v[0]); err != nil {
			return nil, err
		}
		return Exp{v[0]}, nil
	}},
	"const": {[]string{"value"}, func(v []float64) (Dist, error) { return Const{v[0]}, nil }},
}

func parseFloat(s string) (float64, error) {
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0, fmt.Errorf("bad number %q", s)
	}
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, fmt.Errorf("number %q is not finite", s)
	}
	return v, nil
}

// parseDist parses `name(arg, ...)`. Arguments are either all positional or
// all `param=value`.
func parseDist(s string) (Dist, error) {
	s = strings.TrimSpace(s)
	name, rest, ok := strings.Cut(s, "(")
	if !ok || !strings.HasSuffix(rest, ")") {
		return nil, fmt.Errorf("want name(args), got %q", s)
	}
	spec, ok := distSpecs[strings.TrimSpace(name)]
	if !ok {
		return nil, fmt.Errorf("unknown distribution %q", strings.TrimSpace(name))
	}
	args := strings.Split(strings.TrimSuffix(rest, ")"), ",")
	if len(args) != len(spec.params) {
		return nil, fmt.Errorf("%s takes %d arguments (%s), got %d",
			strings.TrimSpace(name), len(spec.params), strings.Join(spec.params, ", "), len(args))
	}
	vals := make([]float64, len(args))
	set := make([]bool, len(args))
	named := strings.Contains(args[0], "=")
	for i, a := range args {
		key, val, hasKey := strings.Cut(a, "=")
		if hasKey != named {
			return nil, fmt.Errorf("mixed positional and named arguments in %q", s)
		}
		idx := i
		if named {
			idx = -1
			for j, p := range spec.params {
				if p == strings.TrimSpace(key) {
					idx = j
				}
			}
			if idx < 0 {
				return nil, fmt.Errorf("unknown parameter %q (want %s)", strings.TrimSpace(key), strings.Join(spec.params, ", "))
			}
			if set[idx] {
				return nil, fmt.Errorf("parameter %q given twice", spec.params[idx])
			}
		} else {
			val = a
		}
		v, err := parseFloat(val)
		if err != nil {
			return nil, err
		}
		vals[idx], set[idx] = v, true
	}
	return spec.build(vals)
}
