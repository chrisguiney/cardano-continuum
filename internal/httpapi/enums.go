package httpapi

import (
	"slices"

	"github.com/chrisguiney/cardano-continuum/api/v1alpha1"
	"github.com/chrisguiney/cardano-continuum/internal/adversary"
	"github.com/chrisguiney/cardano-continuum/internal/engine"
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
}

func strs[T ~string](xs []T) []string {
	out := make([]string, len(xs))
	for i, x := range xs {
		out[i] = string(x)
	}
	return out
}

// BuildEnums collects the allowlists. Intent kinds include the attacks,
// which score as intents.
func BuildEnums() Enums {
	intents := append(strs(profile.IntentKinds()), strs(adversary.Names())...)
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
	}
}
