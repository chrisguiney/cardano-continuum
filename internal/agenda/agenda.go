// Package agenda turns an actor and a read-only view of the world into one
// intent. A Policy scores the intents an actor could attempt; Choose samples
// one in proportion to score times the profile's weight. The scores travel
// with the decision so a reader can see why the actor acted.
//
// The package is pure: it reads no clock and no chain, only the View it is
// handed, and draws randomness only from the stream it is given.
package agenda

import (
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"sort"

	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
)

// Idle is the intent of doing nothing this turn. It is not a profile intent
// kind: it has no rate, only a score.
const Idle profile.IntentKind = "idle"

// Errors returned by For.
var (
	ErrUnknown       = errors.New("unknown agenda")
	ErrUnimplemented = errors.New("agenda not implemented yet")
)

// PoolInfo is one row of the pool return table.
type PoolInfo struct {
	ID population.ActorID
	// ROI is the pool's expected return to delegators per epoch, as a fraction.
	ROI float64
}

// View is the agenda's read-only window onto the chain and world. The engine
// implements it from the chain follower; tests use a fake.
type View interface {
	// Balance is the spendable lovelace of the actor.
	Balance(population.ActorID) profile.Lovelace
	// Pools lists registered pools in any order.
	Pools() []PoolInfo
	// Epoch is the current epoch.
	Epoch() uint64
	// StakeRegistered reports whether the actor's stake key is registered.
	StakeRegistered(population.ActorID) bool
	// DelegatedTo is the pool the actor's stake is delegated to.
	DelegatedTo(population.ActorID) (pool population.ActorID, ok bool)
	// PoolRegistered reports whether the pool actor's pool is registered.
	PoolRegistered(population.ActorID) bool
}

// Score is a policy's appetite for one intent. Value is non-negative; zero
// means the intent is off the table and Why says why. Target is the actor the
// intent is aimed at (a pool to delegate to), or zero.
type Score struct {
	Kind   profile.IntentKind `json:"kind"`
	Value  float64            `json:"value"`
	Target population.ActorID `json:"target,omitempty"`
	Why    string             `json:"why"`
}

// Intent is the chosen action.
type Intent struct {
	Kind   profile.IntentKind `json:"kind"`
	Actor  population.ActorID `json:"actor"`
	Target population.ActorID `json:"target,omitempty"`
}

// Decision is an intent with the scores it was drawn from.
type Decision struct {
	Intent Intent  `json:"intent"`
	Scores []Score `json:"scores"`
}

// Policy scores the intents available to an actor. It must be a pure function
// of its arguments and return each kind at most once.
type Policy interface {
	Score(a population.Actor, v View) []Score
}

// descriptor is a Policy built from a function, so each agenda is data rather
// than a branch in shared code.
type descriptor struct {
	score func(a population.Actor, v View) []Score
}

func (d descriptor) Score(a population.Actor, v View) []Score { return d.score(a, v) }

var registry = map[profile.AgendaName]Policy{
	"hodl":     descriptor{hodlScores},
	"yield":    descriptor{yieldScores},
	"operator": descriptor{operatorScores},
	"disrupt":  descriptor{idleOnly("disrupt agenda is a stub")},
	"extract":  descriptor{idleOnly("extract agenda is a stub")},
}

var pending = map[profile.AgendaName]bool{"profit": true, "civic": true, "opportunist": true}

// For returns the policy for an agenda name.
func For(name profile.AgendaName) (Policy, error) {
	if p, ok := registry[name]; ok {
		return p, nil
	}
	if pending[name] {
		return nil, fmt.Errorf("%w: %s", ErrUnimplemented, name)
	}
	return nil, fmt.Errorf("%w: %q", ErrUnknown, name)
}

// Decide scores the actor's intents and samples one using r.
func Decide(a population.Actor, r *rand.Rand, v View, p Policy, w map[profile.IntentKind]float64) Decision {
	in, ss := Choose(a.ID, r, p.Score(a, v), w)
	return Decision{Intent: in, Scores: ss}
}

// Choose samples an intent with probability proportional to score times the
// weight in w (missing means 1). Scores and weights that are negative, NaN or
// infinite count as zero. When nothing is positive the intent is Idle. The
// returned scores are a copy sorted by kind then target, so the draw does not
// depend on the order the policy produced them in.
func Choose(actor population.ActorID, r *rand.Rand, scores []Score, w map[profile.IntentKind]float64) (Intent, []Score) {
	out := append([]Score(nil), scores...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Target < out[j].Target
	})
	eff := make([]float64, len(out))
	total := 0.0
	for i, s := range out {
		weight := 1.0
		if x, ok := w[s.Kind]; ok {
			weight = x
		}
		eff[i] = finite(s.Value) * finite(weight)
		total += eff[i]
	}
	if total <= 0 || math.IsInf(total, 0) {
		return Intent{Kind: Idle, Actor: actor}, out
	}
	u := r.Float64() * total
	last := -1
	for i, e := range eff {
		if e <= 0 {
			continue
		}
		last = i
		if u < e {
			break
		}
		u -= e
	}
	return Intent{Kind: out[last].Kind, Actor: actor, Target: out[last].Target}, out
}

func finite(x float64) float64 {
	if math.IsNaN(x) || math.IsInf(x, 0) || x < 0 {
		return 0
	}
	return x
}

// Approximate lovelace cost of attempting each intent: deposit or amount
// floor plus a fee allowance. An intent whose cost exceeds the balance scores
// zero so the planner never receives one it must reject.
var cost = map[profile.IntentKind]profile.Lovelace{
	profile.IntentSend:          2_000_000,
	profile.IntentDelegate:      500_000,
	profile.IntentRegisterStake: 3_000_000,
	profile.IntentWithdraw:      500_000,
	profile.IntentRegisterPool:  505_000_000,
	profile.IntentRetirePool:    500_000,
}

// pick scores kind at value, or zero with a reason when the actor cannot pay.
func pick(kind profile.IntentKind, value float64, bal profile.Lovelace, why string) Score {
	if c := cost[kind]; bal < c {
		return Score{Kind: kind, Why: fmt.Sprintf("balance %d below cost %d", bal, c)}
	}
	return Score{Kind: kind, Value: value, Why: why}
}

func off(kind profile.IntentKind, why string) Score { return Score{Kind: kind, Why: why} }

func idleOnly(why string) func(population.Actor, View) []Score {
	return func(population.Actor, View) []Score {
		return []Score{{Kind: Idle, Value: 1, Why: why}}
	}
}
