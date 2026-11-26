package httpapi

import (
	"github.com/chrisguiney/cardano-continuum/internal/adversary"
)

// EpochCost is lovelace an adversary spent in one epoch.
type EpochCost struct {
	Epoch uint64 `json:"epoch"`
	Spent uint64 `json:"spent"`
}

// CostView is an adversary's spend against its per-epoch ceiling. Spent is
// the run's total; Epoch is the adversary's latest epoch with spend and
// EpochSpent that epoch's, the share the ceiling binds. Ceiling is zero when no store row names it.
type CostView struct {
	Spent      uint64      `json:"spent"`
	Ceiling    uint64      `json:"ceiling"`
	Epoch      uint64      `json:"epoch"`
	EpochSpent uint64      `json:"epoch_spent"`
	ByEpoch    []EpochCost `json:"by_epoch"`
}

// SuccessView is an adversary's success metric: its name, the run's value
// and the control run's, when one was supplied.
type SuccessView struct {
	Name    string   `json:"name"`
	Value   float64  `json:"value"`
	Control *float64 `json:"control"`
}

// AttackStepRow is one recent attack decision.
type AttackStepRow struct {
	Seq      uint64 `json:"seq"`
	At       int64  `json:"at"`
	Intent   uint64 `json:"intent"`
	Mutation string `json:"mutation,omitempty"`
	Cost     uint64 `json:"cost"`
	Value    uint64 `json:"value,omitempty"`
	Txs      int    `json:"txs"`
}

// AdversaryRow is one adversary actor. Detected counts attacks a node
// rejected or the oracle flagged, Rejected those a node rejected.
type AdversaryRow struct {
	ID        uint64          `json:"id"`
	Name      string          `json:"name"`
	Profile   string          `json:"profile"`
	Attack    string          `json:"attack"`
	Agenda    string          `json:"agenda"`
	Budget    uint64          `json:"budget"`
	Cost      CostView        `json:"cost"`
	Metric    SuccessView     `json:"metric"`
	Attempted int             `json:"attempted"`
	Landed    int             `json:"landed"`
	Detected  int             `json:"detected"`
	Rejected  int             `json:"rejected"`
	Recent    []AttackStepRow `json:"recent"`
	Findings  []ActorFinding  `json:"findings"`
}

// ImpactRow is one run metric: the run's value, and the control run's with
// the difference when a control was supplied. Honest-side metrics (failure
// rate, retries, UTxO growth, value grabbed) are the attack's impact.
type ImpactRow struct {
	Metric  string   `json:"metric"`
	Value   float64  `json:"value"`
	Control *float64 `json:"control"`
	Delta   *float64 `json:"delta"`
}

// AdversaryView is the Adversaries page.
type AdversaryView struct {
	HasControl  bool           `json:"has_control"`
	Adversaries []AdversaryRow `json:"adversaries"`
	Impact      []ImpactRow    `json:"impact"`
}

func mapKeys(m map[uint64]bool) func(func(uint64) bool) {
	return func(yield func(uint64) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

func impactRows(ctl, cur adversary.Metrics, has bool) []ImpactRow {
	rows := adversary.Compare(ctl, cur)
	out := make([]ImpactRow, len(rows))
	for i, r := range rows {
		out[i] = ImpactRow{Metric: r.Metric, Value: r.Treated}
		if has {
			c, d := r.Control, r.Delta()
			out[i].Control, out[i].Delta = &c, &d
		}
	}
	return out
}
