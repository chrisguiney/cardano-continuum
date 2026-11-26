package mistake

import (
	"math"
	"math/rand/v2"
	"slices"

	"github.com/chrisguiney/cardano-continuum/internal/profile"
	"github.com/chrisguiney/cardano-continuum/internal/submit"
)

// Rate is the chance an actor makes a mistake on one transaction:
// (1 - competence) * (1 - care), each clamped to [0, 1]. A competent or a
// careful actor alone is enough to avoid most mistakes.
func Rate(t profile.SampledTraits) float64 {
	return (1 - unit(t.Competence)) * (1 - unit(t.Care))
}

func unit(x float64) float64 {
	if math.IsNaN(x) {
		return 0
	}
	return min(max(x, 0), 1)
}

// Sample decides whether the actor makes a mistake on this transaction and
// which, uniformly among the applicable kinds. It draws exactly two values
// from r whatever the outcome, so an actor's stream stays aligned across
// runs that differ only in what was applicable.
func Sample(t profile.SampledTraits, applicable KindSet, r *rand.Rand) KindSet {
	u, v := r.Float64(), r.Float64()
	ks := applicable.Kinds()
	if len(ks) == 0 || u >= Rate(t) {
		return 0
	}
	return Of(ks[pick(v, len(ks))])
}

// pick maps v in [0, 1) to an index below n.
func pick(v float64, n int) int { return min(int(v*float64(n)), n-1) }

// diagnosable is every class an actor may believe it got wrong: the
// catalog's classes and the mempool's "inputs spent".
var diagnosable = func() []submit.Class {
	var cs []submit.Class
	for _, k := range Kinds() {
		if c, ok := ClassOf(k); ok {
			cs = append(cs, c)
		}
	}
	cs = append(cs, spentClasses...)
	slices.Sort(cs)
	return slices.Compact(cs)
}()

// Diagnoser is an actor reading a rejection. With probability Competence it
// names one of the reported classes; otherwise any diagnosable class, which
// may by chance be a reported one. So accuracy is Competence plus the
// chance of a lucky guess, and rises strictly with Competence. It
// implements submit.Diagnoser: the class chooses the fix and nothing else.
type Diagnoser struct{ Competence float64 }

// Diagnose implements submit.Diagnoser. It draws exactly two values from r.
func (d Diagnoser) Diagnose(r *rand.Rand, failures []submit.Failure) submit.Class {
	u, v := r.Float64(), r.Float64()
	if len(failures) == 0 {
		return submit.Unknown
	}
	if u < unit(d.Competence) {
		return failures[pick(v, len(failures))].Class
	}
	return diagnosable[pick(v, len(diagnosable))]
}
