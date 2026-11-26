// Package meter folds an engine log into analytics: the adversary comparison
// metrics, per-attack tallies and the payment audit. It reads records only and
// never touches an Engine.
package meter

import (
	"cmp"
	"context"
	"fmt"
	"slices"

	"github.com/chrisguiney/cardano-continuum/internal/adversary"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/wire"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/oracle"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

// Measure folds a run's log into the metrics adversary.Compare reads: what
// happened to honest intents, to the UTxO set and the node's answers, and
// what the adversaries spent and how many of their steps were caught.
// Honest means no adversary's: Submitted.Attack is empty and the intent is
// no refill.
func Measure(ctx context.Context, r eventlog.Reader) (adversary.Metrics, error) {
	m := newMeter()
	err := r.Scan(ctx, 1, func(rec eventlog.Record) error {
		if err := m.add(rec); err != nil {
			return fmt.Errorf("engine: measure seq %d: %w", rec.Seq, err)
		}
		return nil
	})
	m.AttackDetected = len(m.detected)
	return m.Metrics, err
}

type meter struct {
	adversary.Metrics
	// first is each honest intent's first submission; attacks maps each
	// attack intent to its transactions, attackTx the reverse.
	first    map[uint64]sched.Time
	attacks  map[uint64]bool
	attackTx map[wire.TxID]uint64
	detected map[uint64]bool
	// value is what each attack takes from an honest lock if it lands.
	value map[uint64]uint64
	// by tallies each adversary actor's steps; owner maps an attack intent
	// to its actor and rejected holds the attack intents a node refused.
	by       map[uint64]*AttackStat
	owner    map[uint64]uint64
	rejected map[uint64]bool
	// blocks are the applied blocks still on the chain, with what each
	// added to the UTxO set, so a rollback can take it off again.
	blocks []blockDelta
}

type blockDelta struct {
	slot  uint64
	delta int64
}

func newMeter() *meter {
	return &meter{first: map[uint64]sched.Time{}, attacks: map[uint64]bool{}, attackTx: map[wire.TxID]uint64{}, detected: map[uint64]bool{}, value: map[uint64]uint64{},
		by: map[uint64]*AttackStat{}, owner: map[uint64]uint64{}, rejected: map[uint64]bool{}}
}

func (m *meter) add(rec eventlog.Record) error {
	if rec.Class == eventlog.Input {
		return m.input(rec)
	}
	if rec.Class != eventlog.Decision {
		return nil
	}
	switch rec.Kind {
	case wire.KindSubmitted:
		var s wire.Submitted
		if err := eventlog.Decode(rec.Body, &s); err != nil {
			return err
		}
		m.submitted(rec.At, s)
	case wire.KindAttack:
		var a wire.Attack
		if err := eventlog.Decode(rec.Body, &a); err != nil {
			return err
		}
		m.AttackSteps++
		m.AttackCost += a.Cost
		m.tally(rec, a)
		m.attacks[a.Intent] = true
		m.value[a.Intent] = a.Value
		for _, tx := range a.Txs {
			m.attackTx[tx] = a.Intent
		}
	case wire.KindConfirmed:
		var c wire.Confirmed
		if err := eventlog.Decode(rec.Body, &c); err != nil {
			return err
		}
		m.ended(rec.At, c.Intent, false)
	case wire.KindReleased:
		var rl wire.Released
		if err := eventlog.Decode(rec.Body, &rl); err != nil {
			return err
		}
		m.ended(rec.At, rl.Intent, true)
	case oracle.KindFinding:
		return m.finding(rec)
	}
	return nil
}

func (m *meter) submitted(at sched.Time, s wire.Submitted) {
	if s.Attack != "" {
		return
	}
	if _, ok := m.first[s.Intent]; ok {
		m.Retries++
		return
	}
	m.first[s.Intent] = at
	m.HonestIntents++
}

func (m *meter) ended(at sched.Time, intent uint64, released bool) {
	if m.attacks[intent] {
		if !released {
			m.AttackLanded++
			m.Grabbed += m.value[intent]
			m.by[m.owner[intent]].Landed++
		}
		return
	}
	start, ok := m.first[intent]
	if !ok {
		return
	}
	m.HonestEnded++
	if released {
		m.HonestFailed++
		return
	}
	m.Latency = append(m.Latency, float64(at-start)/1000)
}

func (m *meter) finding(rec eventlog.Record) error {
	f, err := oracle.Decode(rec)
	if err != nil {
		return err
	}
	m.Findings++
	if f.Severity == oracle.Fatal {
		m.Fatal++
	}
	var id wire.TxID
	if len(f.TxHash) == len(id) {
		copy(id[:], f.TxHash)
		if intent, ok := m.attackTx[id]; ok {
			m.detected[intent] = true
		}
	}
	return nil
}

func (m *meter) input(rec eventlog.Record) error {
	switch rec.Kind {
	case eventlog.KindSubmitResult:
		var r wire.SubmitResult
		if err := eventlog.Decode(rec.Body, &r); err != nil {
			return err
		}
		m.answers(r)
	case eventlog.KindBlockApplied:
		var b eventlog.BlockApplied
		if err := eventlog.Decode(rec.Body, &b); err != nil {
			return err
		}
		var c wire.BlockContent
		if len(b.Content) > 0 {
			if err := eventlog.Decode(b.Content, &c); err != nil {
				return err
			}
		}
		var d int64
		for _, tx := range c.Txs {
			d += int64(len(tx.Outputs)) - int64(len(tx.Inputs))
		}
		m.blocks = append(m.blocks, blockDelta{slot: b.Point.Slot, delta: d})
		m.UtxoGrowth += d
	case eventlog.KindRolledBack:
		var b eventlog.RolledBack
		if err := eventlog.Decode(rec.Body, &b); err != nil {
			return err
		}
		for len(m.blocks) > 0 && m.blocks[len(m.blocks)-1].slot > b.To.Slot {
			m.UtxoGrowth -= m.blocks[len(m.blocks)-1].delta
			m.blocks = m.blocks[:len(m.blocks)-1]
		}
	}
	return nil
}

func (m *meter) answers(r wire.SubmitResult) {
	rejected := 0
	if r.Verdict == wire.VerdictRejected {
		rejected++
	}
	for _, a := range append(again(r), r.Rivals...) {
		if a.Verdict == wire.VerdictRejected {
			rejected++
		}
	}
	m.Rejections += rejected
	if rejected > 0 && m.attacks[r.Intent] {
		m.detected[r.Intent] = true
		m.rejected[r.Intent] = true
	}
}

// again is a DoubleSubmit's second answer, if any.
func again(r wire.SubmitResult) []wire.Answer {
	if r.Again == nil {
		return nil
	}
	return []wire.Answer{*r.Again}
}

// AttackStep is one logged attack step.
type AttackStep struct {
	Seq      uint64
	At       int64
	Intent   uint64
	Mutation string
	Cost     uint64
	Value    uint64
	Txs      int
}

// CostPoint is lovelace charged at a time, so a caller can bucket spend by
// epoch.
type CostPoint struct {
	At   int64
	Cost uint64
}

// AttackStat is one adversary actor's tally. Detected counts its steps a
// node rejected or the oracle flagged, of which Rejected were rejected.
type AttackStat struct {
	Actor                             uint64
	Name                              string
	Steps, Landed, Detected, Rejected int
	Cost                              uint64
	Costs                             []CostPoint
	Recent                            []AttackStep
}

// maxRecentSteps bounds the steps kept per adversary.
const maxRecentSteps = 10

func (m *meter) tally(rec eventlog.Record, a wire.Attack) {
	actor := uint64(rec.Actor)
	st := m.by[actor]
	if st == nil {
		st = &AttackStat{Actor: actor}
		m.by[actor] = st
	}
	st.Name, st.Steps, st.Cost = a.Name, st.Steps+1, st.Cost+a.Cost
	st.Costs = append(st.Costs, CostPoint{At: int64(rec.At), Cost: a.Cost})
	st.Recent = append(st.Recent, AttackStep{Seq: rec.Seq, At: int64(rec.At), Intent: a.Intent, Mutation: a.Mutation, Cost: a.Cost, Value: a.Value, Txs: len(a.Txs)})
	if len(st.Recent) > maxRecentSteps {
		st.Recent = slices.Delete(st.Recent, 0, len(st.Recent)-maxRecentSteps)
	}
	m.owner[a.Intent] = actor
}

// Meter folds records into Measure's metrics one at a time, so a live
// server can keep them current. It is not safe for concurrent use.
type Meter struct{ m *meter }

// NewMeter returns an empty Meter.
func NewMeter() *Meter { return &Meter{m: newMeter()} }

// Add folds one record; records of other kinds are ignored.
func (x *Meter) Add(rec eventlog.Record) error { return x.m.add(rec) }

// Metrics are the metrics of the records added so far.
func (x *Meter) Metrics() adversary.Metrics {
	out := x.m.Metrics
	out.AttackDetected = len(x.m.detected)
	return out
}

// Attacks is each adversary's tally, by actor.
func (x *Meter) Attacks() []AttackStat {
	out := make([]AttackStat, 0, len(x.m.by))
	for _, st := range x.m.by {
		c := *st
		c.Costs, c.Recent = slices.Clone(st.Costs), slices.Clone(st.Recent)
		out = append(out, c)
	}
	for i := range out {
		for intent := range x.m.detected {
			if x.m.owner[intent] == out[i].Actor {
				out[i].Detected++
			}
		}
		for intent := range x.m.rejected {
			if x.m.owner[intent] == out[i].Actor {
				out[i].Rejected++
			}
		}
	}
	slices.SortFunc(out, func(a, b AttackStat) int { return cmp.Compare(a.Actor, b.Actor) })
	return out
}

// AttackActor is the adversary that sent the transaction in an attack step.
func (x *Meter) AttackActor(tx []byte) (uint64, bool) {
	if len(tx) != len(wire.TxID{}) {
		return 0, false
	}
	var id wire.TxID
	copy(id[:], tx)
	intent, ok := x.m.attackTx[id]
	if !ok {
		return 0, false
	}
	return x.m.owner[intent], true
}
