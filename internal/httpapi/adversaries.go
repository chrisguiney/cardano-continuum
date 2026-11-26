package httpapi

import (
	"context"
	"slices"
	"strings"

	"github.com/chrisguiney/cardano-continuum/internal/adversary"
	"github.com/chrisguiney/cardano-continuum/internal/engine"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
	"github.com/chrisguiney/cardano-continuum/internal/store"
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

// Attacks returns the meter's tallies and metrics.
func (p *Projection) Attacks() ([]engine.AttackStat, adversary.Metrics, int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.meter.Attacks(), p.meter.Metrics(), p.now
}

// attackOwner is the adversary that sent tx, by an Attack decision or by
// its own Submitted record.
func (p *Projection) attackOwner(tx []byte) (uint64, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if id, ok := p.meter.AttackActor(tx); ok {
		return id, true
	}
	if len(tx) != 32 {
		return 0, false
	}
	id, ok := p.txOwner[[32]byte(tx)]
	return id, ok
}

// Adversaries implements Source.
func (s *LogSource) Adversaries(ctx context.Context) (AdversaryView, error) {
	stats, cur, now := s.cfg.Projection.Attacks()
	n, err := s.names(ctx)
	if err != nil {
		return AdversaryView{}, err
	}
	idents := s.identities()
	ids := map[uint64]bool{}
	for _, st := range stats {
		ids[st.Actor] = true
	}
	for id, in := range idents {
		if in.Kind == "adversary" {
			ids[id] = true
		}
	}
	var ctl adversary.Metrics
	if s.cfg.Control != nil {
		ctl = *s.cfg.Control
	}
	impact := impactRows(ctl, cur, s.cfg.Control != nil)
	out := AdversaryView{HasControl: s.cfg.Control != nil, Adversaries: []AdversaryRow{}, Impact: impact}
	for _, id := range slices.Sorted(mapKeys(ids)) {
		var st engine.AttackStat
		for _, x := range stats {
			if x.Actor == id {
				st = x
			}
		}
		out.Adversaries = append(out.Adversaries, s.adversaryRow(id, st, n[id], idents[id], impact, now))
	}
	return out, nil
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

func (s *LogSource) adversaryRow(id uint64, st engine.AttackStat, row store.ActorRow, in store.Identity, impact []ImpactRow, now int64) AdversaryRow {
	r := AdversaryRow{ID: id, Name: row.Name, Profile: row.Profile, Attack: st.Name, Agenda: in.Agenda, Budget: in.Budget,
		Attempted: st.Steps, Landed: st.Landed, Detected: st.Detected, Rejected: st.Rejected,
		Recent: []AttackStepRow{}, Findings: s.adversaryFindings(id)}
	if r.Attack == "" {
		r.Attack = strings.TrimPrefix(row.Profile, "adversary-")
	}
	for _, x := range st.Recent {
		r.Recent = append(r.Recent, AttackStepRow{Seq: x.Seq, At: x.At, Intent: x.Intent, Mutation: x.Mutation, Cost: x.Cost, Value: x.Value, Txs: x.Txs})
	}
	r.Cost = s.costView(st, in.CostCeiling, now)
	r.Metric = SuccessView{Name: in.Metric}
	if r.Metric.Name == "" {
		r.Metric.Name = adversary.SuccessMetric(adversary.Name(r.Attack))
	}
	for _, i := range impact {
		if i.Metric == r.Metric.Name {
			r.Metric.Value, r.Metric.Control = i.Value, i.Control
		}
	}
	return r
}

func (s *LogSource) costView(st engine.AttackStat, ceiling uint64, now int64) CostView {
	c := CostView{Spent: st.Cost, Ceiling: ceiling, ByEpoch: []EpochCost{}}
	epochOf := func(int64) uint64 { return 0 }
	if s.cfg.Slots != nil {
		epochOf = func(at int64) uint64 { return s.cfg.Slots.EpochAt(sched.Time(at)) }
	}
	c.Epoch = epochOf(now)
	if n := len(st.Costs); n > 0 {
		c.Epoch = epochOf(st.Costs[n-1].At)
	}
	for _, p := range st.Costs {
		e := epochOf(p.At)
		if n := len(c.ByEpoch); n > 0 && c.ByEpoch[n-1].Epoch == e {
			c.ByEpoch[n-1].Spent += p.Cost
		} else {
			c.ByEpoch = append(c.ByEpoch, EpochCost{Epoch: e, Spent: p.Cost})
		}
		if e == c.Epoch {
			c.EpochSpent += p.Cost
		}
	}
	return c
}

// adversaryFindings lists the findings on transactions the adversary sent.
func (s *LogSource) adversaryFindings(id uint64) []ActorFinding {
	fs := s.findingsSet()
	_, n := fs.Page(0, 0)
	all, _ := fs.Page(0, n)
	out := []ActorFinding{}
	for _, f := range all {
		if owner, ok := s.cfg.Projection.attackOwner(f.TxHash); ok && owner == id {
			out = append(out, ActorFinding{ID: f.ID, Seq: f.Seq, Kind: string(f.Kind), Severity: f.Severity.String(), Summary: strings.TrimSpace(f.Summary)})
		}
	}
	return out
}
