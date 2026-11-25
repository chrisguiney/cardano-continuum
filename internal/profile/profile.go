// Package profile parses and validates actor profiles and samples per-actor
// traits from them. It does no I/O of its own: input arrives through an
// io.Reader and randomness through a Source.
package profile

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
	"hodl": true, "yield": true, "profit": true, "civic": true,
	"operator": true, "opportunist": true, "disrupt": true, "extract": true,
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
)

var intents = map[IntentKind]bool{
	IntentSend: true, IntentDelegate: true, IntentUndelegate: true, IntentRegisterStake: true,
	IntentWithdraw: true, IntentVote: true, IntentRegisterDRep: true, IntentDelegateDRep: true,
	IntentPropose: true, IntentMint: true, IntentBurn: true, IntentScriptCall: true,
	IntentRegisterPool: true, IntentRetirePool: true,
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
}

// SampledTraits are one actor's drawn traits.
type SampledTraits struct {
	Competence, Patience, LearningRate, Care float64
}

// Sample draws one actor's traits. The draw order is fixed (competence,
// patience, learning rate, care) so a given stream reproduces the same actor.
func (t Traits) Sample(src Source) SampledTraits {
	return SampledTraits{
		Competence:   t.Competence.Sample(src),
		Patience:     t.Patience.Sample(src),
		LearningRate: t.LearningRate.Sample(src),
		Care:         t.Care.Sample(src),
	}
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
}
