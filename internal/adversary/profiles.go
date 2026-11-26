package adversary

import (
	"bytes"
	"fmt"
	"text/template"

	"github.com/chrisguiney/cardano-continuum/internal/profile"
)

// The built-in adversary profiles, one per attack, that `continuum run
// --attack` adds to a population. Each holds its own budget, acts about
// every 10 seconds, mostly attacking, the rest cover traffic, and may spend at
// most CostCeiling per epoch. Competence is high but below 1: a Beta must
// have B > 0.
var profileTmpl = template.Must(template.New("adv").Parse(`version: 1
profiles:
{{- range .}}
  - name: {{.Profile}}
    kind: wallet
    agenda: disrupt
    traits:
      competence: beta(1000, 1)
      patience: const(1)
      learning_rate: const(0)
      care: const(1)
    budget: {{.Budget}}
    rates:
      base:
        send: 0.1
    adversary:
      attacks: [{{.Attack}}]
      cost_ceiling: {{.Ceiling}}
      success_metric: {{.Success}}
{{- end}}
`))

// Budget and CostCeiling of the built-in profiles, in lovelace.
const (
	Budget      = 5_000_000_000
	CostCeiling = 300_000_000
)

var success = map[Name]string{
	DustFlood:   "utxo_growth",
	Contention:  "honest_value_grabbed_ada",
	Malformed:   "fatal_findings",
	FrontRun:    "honest_failure_rate",
	DoubleSpend: "attack_landed",
	ExUnitsGap:  "honest_latency_p99_s",
}

// SuccessMetric is the metric attack n's built-in profile declares.
func SuccessMetric(n Name) string { return success[n] }

// ProfileName is the built-in profile of attack n.
func ProfileName(n Name) string { return "adversary-" + string(n) }

// Profiles returns the built-in profiles of the attacks ns.
func Profiles(ns ...Name) ([]profile.Profile, error) {
	type row struct {
		Profile, Success string
		Attack           Name
		Budget, Ceiling  uint64
	}
	var rows []row
	for _, n := range ns {
		if _, err := Parse(string(n)); err != nil {
			return nil, err
		}
		rows = append(rows, row{Profile: ProfileName(n), Attack: n, Success: success[n], Budget: Budget, Ceiling: CostCeiling})
	}
	var b bytes.Buffer
	if err := profileTmpl.Execute(&b, rows); err != nil {
		return nil, fmt.Errorf("adversary: profiles: %w", err)
	}
	return profile.ParseNamed("adversary", &b)
}
