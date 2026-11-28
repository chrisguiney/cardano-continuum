package view

import (
	"slices"

	"github.com/chrisguiney/cardano-continuum/api/v1alpha1"
	"github.com/chrisguiney/cardano-continuum/internal/adversary"
	"github.com/chrisguiney/cardano-continuum/internal/agenda"
	"github.com/chrisguiney/cardano-continuum/internal/chain"
	"github.com/chrisguiney/cardano-continuum/internal/engine"
	"github.com/chrisguiney/cardano-continuum/internal/household"
	"github.com/chrisguiney/cardano-continuum/internal/mistake"
	"github.com/chrisguiney/cardano-continuum/internal/oracle"
	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
)

// Enums are the finite value sets the UI offers as dropdowns. Each comes
// from the list the server validates the same parameter against, so the UI
// cannot offer a value the API refuses or miss one it accepts. Profile
// names are not here: they come from the loaded profiles, so the UI reads
// them from the overview.
type Enums struct {
	ActorKind    []string `json:"actor_kind"`
	Agenda       []string `json:"agenda"`
	State        []string `json:"state"`
	IntentKind   []string `json:"intent_kind"`
	FindingKind  []string `json:"finding_kind"`
	Severity     []string `json:"severity"`
	Impl         []string `json:"impl"`
	Tier         []string `json:"tier"`
	Phase        []string `json:"phase"`
	Attack       []string `json:"attack"`
	DecisionKind []string `json:"decision_kind"`
	// VoterClass and ActionType are the Governance view's voter classes
	// and proposal types.
	VoterClass []string `json:"voter_class"`
	ActionType []string `json:"action_type"`
	// MistakeKind is internal/mistake's catalog, the kinds a Submitted
	// decision's mistakes name.
	MistakeKind []string `json:"mistake_kind"`
	// Weight is the intent kinds a set_weights control overrides, one
	// form field each on the controls page.
	Weight []string `json:"weight"`
	// Scenario is every scenario an inject control may name, with its
	// argument and what it does.
	Scenario []engine.ScenarioInfo `json:"scenarios"`
	// Purpose is the wallet purposes and InvestStyle the humans'
	// investment styles.
	Purpose     []string `json:"purpose"`
	InvestStyle []string `json:"invest_style"`
}

func strs[T ~string](xs []T) []string {
	out := make([]string, len(xs))
	for i, x := range xs {
		out[i] = string(x)
	}
	return out
}

func mistakeKinds() []string {
	ks := mistake.Kinds()
	out := make([]string, len(ks))
	for i, k := range ks {
		out[i] = k.String()
	}
	return out
}

// BuildEnums collects the allowlists. Intent kinds include the attacks,
// which score as intents, and the agenda's own intents (the committee's,
// the pool lifecycle steps, idle), which have no profile rate.
func BuildEnums() Enums {
	intents := append(strs(profile.IntentKinds()), strs(adversary.Names())...)
	intents = append(intents, strs(agenda.Kinds())...)
	slices.Sort(intents)
	sev := make([]string, 0, 3)
	for _, s := range oracle.Severities() {
		sev = append(sev, s.String())
	}
	return Enums{
		ActorKind: engine.ActorKinds(), Agenda: strs(profile.Agendas()), State: strs(population.States()),
		IntentKind: intents, FindingKind: strs(oracle.Kinds()), Severity: sev,
		Impl: strs(v1alpha1.Implementations()), Tier: strs(v1alpha1.Tiers()), Phase: strs(v1alpha1.Phases()),
		Attack: strs(adversary.Names()), DecisionKind: engine.DecisionKinds(),
		VoterClass: []string{classCommittee, classDRep, classSPO}, ActionType: chain.ActionTypes(),
		MistakeKind: mistakeKinds(), Weight: strs(profile.IntentKinds()), Scenario: engine.Scenarios(),
		Purpose: strs(population.Purposes()), InvestStyle: strs(household.Styles()),
	}
}
