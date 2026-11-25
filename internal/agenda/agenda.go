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

// Intents that are not profile intent kinds: they have no rate, only a
// score. GoDark and GoLive are the operator's pool lifecycle steps, taken by
// the engine rather than by a transaction.
const (
	Idle   profile.IntentKind = "idle"
	GoDark profile.IntentKind = "go_dark"
	GoLive profile.IntentKind = "go_live"
)

// ErrUnknown is returned by For for a name with no agenda.
var ErrUnknown = errors.New("unknown agenda")

// PoolInfo is one row of the pool return table.
type PoolInfo struct {
	ID population.ActorID
	// ROI is the pool's expected return to delegators per epoch, as a fraction.
	ROI float64
	// Stake is the lovelace delegated to the pool now.
	Stake profile.Lovelace
	// Saturation is the stake above which the pool's rewards stop growing;
	// zero means no cap is known.
	Saturation profile.Lovelace
}

// View is the agenda's read-only window onto the chain and world. The engine
// implements it from the chain follower; tests use a fake.
//
// A View may also implement Floor, Pods, Governance and Market. Agendas
// that need one and find it missing fail closed: they offer no step that
// could lower forging stake, and no vote.
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
// intent is aimed at (a pool to delegate to), or zero; Proposal is the
// governance action a vote is on, or empty.
type Score struct {
	Kind     profile.IntentKind `json:"kind"`
	Value    float64            `json:"value"`
	Target   population.ActorID `json:"target,omitempty"`
	Proposal ProposalID         `json:"proposal,omitempty"`
	Why      string             `json:"why"`
}

// Intent is the chosen action.
type Intent struct {
	Kind     profile.IntentKind `json:"kind"`
	Actor    population.ActorID `json:"actor"`
	Target   population.ActorID `json:"target,omitempty"`
	Proposal ProposalID         `json:"proposal,omitempty"`
}

// Decision is an intent with the scores it was drawn from.
type Decision struct {
	Intent Intent  `json:"intent"`
	Scores []Score `json:"scores"`
}

// Policy scores the intents available to an actor. It must be a pure function
// of its arguments and return each kind, target and proposal at most once.
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
	"hodl":        descriptor{hodlScores},
	"yield":       descriptor{yieldScores},
	"profit":      descriptor{profitScores},
	"civic":       descriptor{civicScores},
	"operator":    descriptor{operatorScores},
	"opportunist": descriptor{opportunistScores},
	"disrupt":     adversary{cover: disruptCover},
	"extract":     adversary{cover: extractCover},
}

// For returns the policy for an agenda name.
func For(name profile.AgendaName) (Policy, error) {
	if p, ok := registry[name]; ok {
		return p, nil
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
// returned scores are a copy sorted by kind, target and proposal, so the draw does not
// depend on the order the policy produced them in.
func Choose(actor population.ActorID, r *rand.Rand, scores []Score, w map[profile.IntentKind]float64) (Intent, []Score) {
	out := append([]Score(nil), scores...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		if out[i].Target != out[j].Target {
			return out[i].Target < out[j].Target
		}
		return out[i].Proposal < out[j].Proposal
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
	return Intent{Kind: out[last].Kind, Actor: actor, Target: out[last].Target, Proposal: out[last].Proposal}, out
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
	profile.IntentRegisterDRep:  500_500_000,
	profile.IntentDelegateDRep:  500_000,
	profile.IntentVote:          500_000,
	profile.IntentScriptCall:    2_000_000,
	profile.IntentMint:          2_000_000,
}

// pick scores kind at value, or zero with a reason when the actor cannot pay.
func pick(kind profile.IntentKind, value float64, bal profile.Lovelace, why string) Score {
	if c := cost[kind]; bal < c {
		return Score{Kind: kind, Why: fmt.Sprintf("balance %d below cost %d", bal, c)}
	}
	return Score{Kind: kind, Value: value, Why: why}
}

func off(kind profile.IntentKind, why string) Score { return Score{Kind: kind, Why: why} }
