// Package profile parses and validates actor profiles and samples per-actor
// traits from them. It does no I/O of its own: input arrives through an
// io.Reader and randomness through a Source.
package profile

import "slices"

// Kind is the type of actor a profile describes.
type Kind string

// Actor kinds.
const (
	Wallet   Kind = "wallet"
	Pool     Kind = "pool"
	DRep     Kind = "drep"
	Contract Kind = "contract"
	Faucet   Kind = "faucet"
)

var kinds = map[Kind]bool{Wallet: true, Pool: true, DRep: true, Contract: true, Faucet: true}

// AgendaName selects the policy that scores an actor's intents.
type AgendaName string

var agendas = map[AgendaName]bool{
	"hodl": true, "dapp": true, "yield": true, "profit": true, "civic": true,
	"operator": true, "opportunist": true, "disrupt": true, "extract": true,
	"grantee": true,
}

// IntentKind names something an actor can try to do.
type IntentKind string

// Intent kinds that rates and weights may refer to.
const (
	IntentSend          IntentKind = "send"
	IntentDelegate      IntentKind = "delegate"
	IntentUndelegate    IntentKind = "undelegate"
	IntentRegisterStake IntentKind = "register_stake"
	IntentWithdraw      IntentKind = "withdraw_rewards"
	IntentVote          IntentKind = "vote"
	IntentRegisterDRep  IntentKind = "register_drep"
	IntentDelegateDRep  IntentKind = "delegate_drep"
	IntentPropose       IntentKind = "propose"
	IntentMint          IntentKind = "mint"
	IntentBurn          IntentKind = "burn"
	IntentScriptCall    IntentKind = "script_call"
	IntentRegisterPool  IntentKind = "register_pool"
	IntentRetirePool    IntentKind = "retire_pool"
	// IntentUpdatePool re-registers a registered pool with new parameters.
	IntentUpdatePool IntentKind = "update_pool"
	// IntentRetireDRep deregisters a DRep, refunding its deposit.
	IntentRetireDRep IntentKind = "retire_drep"
	// IntentBuyADA buys ADA with fiat on an exchange, which pays it out on
	// chain; IntentSellADA pays ADA to an exchange for fiat.
	IntentBuyADA  IntentKind = "buy_ada"
	IntentSellADA IntentKind = "sell_ada"
)

var intents = map[IntentKind]bool{
	IntentSend: true, IntentDelegate: true, IntentUndelegate: true, IntentRegisterStake: true,
	IntentWithdraw: true, IntentVote: true, IntentRegisterDRep: true, IntentDelegateDRep: true,
	IntentPropose: true, IntentMint: true, IntentBurn: true, IntentScriptCall: true,
	IntentRegisterPool: true, IntentRetirePool: true, IntentUpdatePool: true, IntentRetireDRep: true,
	IntentBuyADA: true, IntentSellADA: true,
}

// Lovelace is an amount of ADA in lovelace.
type Lovelace uint64

// Traits are the distributions an actor's personal traits are drawn from.
// Competence is a Beta because it is a probability.
type Traits struct {
	Competence   Beta
	Patience     Dist
	LearningRate Dist
	Care         Dist
	// LossAversion is how readily the actor sells ADA under water, in
	// [0, 1]; nil means DefaultLossAversion, with no draw.
	LossAversion Dist
}

// DefaultLossAversion is the loss aversion of a profile that sets none.
const DefaultLossAversion = 0.5

// SampledTraits are one actor's drawn traits.
type SampledTraits struct {
	Competence, Patience, LearningRate, Care float64
	// LossAversion is the actor's loss aversion.
	LossAversion float64
}

// Sample draws one actor's traits. The draw order is fixed (competence,
// patience, learning rate, care, then loss aversion when the profile sets
// it) so a given stream reproduces the same actor, and a profile without
// loss aversion draws as it did before the trait existed.
func (t Traits) Sample(src Source) SampledTraits {
	s := SampledTraits{
		Competence:   t.Competence.Sample(src),
		Patience:     t.Patience.Sample(src),
		LearningRate: t.LearningRate.Sample(src),
		Care:         t.Care.Sample(src),
		LossAversion: DefaultLossAversion,
	}
	if t.LossAversion != nil {
		s.LossAversion = t.LossAversion.Sample(src)
	}
	return s
}

// Hawkes parameterises self-excitation of an actor's activity. The zero value
// means none. Alpha < Beta keeps the process stationary.
type Hawkes struct{ Alpha, Beta float64 }

// Defaults for the optional population parameters. They are the values the
// population package used before profiles could set them, so a profile that
// leaves them out behaves as before.
const (
	// DefaultOnset is the per-second rate at which an unfrustrated active
	// wallet goes dormant: a mean of one hour.
	DefaultOnset = 1.0 / 3600
	// DefaultTail is the Pareto tail index of the per-actor activity
	// multiplier.
	DefaultTail = 1.5
)

// Rates are an actor's activity rates.
type Rates struct {
	// Base is the per-intent base rate in events per unit of simulated time.
	Base map[IntentKind]float64
	// Diurnal is the amplitude in [0, 1] of the daily rate modulation.
	Diurnal float64
	Hawkes  Hawkes
	// Tail is the Pareto tail index (above 1) of the multiplier that scales
	// each actor's base rates, so a few actors are far busier than the rest.
	// Zero means DefaultTail.
	Tail float64
}

// TailIndex is Tail, or DefaultTail when it is unset.
func (r Rates) TailIndex() float64 {
	if r.Tail == 0 {
		return DefaultTail
	}
	return r.Tail
}

// Hazards drive the actor lifecycle.
type Hazards struct {
	Birth    float64 // new-actor rate
	Dormancy Dist    // dormancy duration; nil means actors never go dormant
	Abandon  float64 // per-unit-time probability of leaving for good
	// Onset is the per-second rate at which an unfrustrated active actor
	// goes dormant; frustration scales it. Zero means DefaultOnset.
	Onset float64
	// Market makes the dormancy onset and abandonment hazards synthetic
	// in a market run: scaled by the market's death factor (Markets.Deaths)
	// in place of being taken as fitted.
	Market bool
}

// OnsetRate is Onset, or DefaultOnset when it is unset.
func (h Hazards) OnsetRate() float64 {
	if h.Onset == 0 {
		return DefaultOnset
	}
	return h.Onset
}

// AdversarySpec describes an adversarial actor's attacks and limits.
type AdversarySpec struct {
	Attacks       []string
	CostCeiling   Lovelace
	SuccessMetric string
}

// Profile is a validated actor profile.
type Profile struct {
	Name      string
	Kind      Kind
	Agenda    AgendaName
	Traits    Traits
	Budget    Lovelace
	Rates     Rates
	Weights   map[IntentKind]float64
	Hazards   Hazards
	Adversary *AdversarySpec
	// Amount is the payment-size distribution; nil keeps the engine's
	// balance-fraction default.
	Amount *Amount
	// Weight is the profile's share of a population; zero means unset, and a
	// population whose profiles all leave it unset splits evenly.
	Weight float64
	// Eagerness shifts when a pool operator adopts an upgrade wave's image
	// (internal/upgrade): nil means 1, above 1 sooner, below 1 later, 0
	// never.
	Eagerness *float64
}

// Eager is the profile's eagerness, 1 when unset.
func (p Profile) Eager() float64 {
	if p.Eagerness == nil {
		return 1
	}
	return *p.Eagerness
}

// Kinds returns every actor kind, sorted.
func Kinds() []Kind { return trueKeys(kinds) }

// Agendas returns every agenda name, sorted.
func Agendas() []AgendaName { return trueKeys(agendas) }

// IntentKinds returns every intent kind, sorted.
func IntentKinds() []IntentKind { return trueKeys(intents) }

func trueKeys[K ~string](m map[K]bool) []K {
	out := make([]K, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
