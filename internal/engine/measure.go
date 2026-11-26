package engine

import (
	"context"
	"fmt"

	"github.com/chrisguiney/cardano-continuum/internal/adversary"
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
	attackTx map[TxID]uint64
	detected map[uint64]bool
	// value is what each attack takes from an honest lock if it lands.
	value map[uint64]uint64
	// blocks are the applied blocks still on the chain, with what each
	// added to the UTxO set, so a rollback can take it off again.
	blocks []blockDelta
}

type blockDelta struct {
	slot  uint64
	delta int64
}

func newMeter() *meter {
	return &meter{first: map[uint64]sched.Time{}, attacks: map[uint64]bool{}, attackTx: map[TxID]uint64{}, detected: map[uint64]bool{}, value: map[uint64]uint64{}}
}

func (m *meter) add(rec eventlog.Record) error {
	if rec.Class == eventlog.Input {
		return m.input(rec)
	}
	if rec.Class != eventlog.Decision {
		return nil
	}
	switch rec.Kind {
	case KindSubmitted:
		var s Submitted
		if err := eventlog.Decode(rec.Body, &s); err != nil {
			return err
		}
		m.submitted(rec.At, s)
	case KindAttack:
		var a Attack
		if err := eventlog.Decode(rec.Body, &a); err != nil {
			return err
		}
		m.AttackSteps++
		m.AttackCost += a.Cost
		m.attacks[a.Intent] = true
		m.value[a.Intent] = a.Value
		for _, tx := range a.Txs {
			m.attackTx[tx] = a.Intent
		}
	case KindConfirmed:
		var c Confirmed
		if err := eventlog.Decode(rec.Body, &c); err != nil {
			return err
		}
		m.ended(rec.At, c.Intent, false)
	case KindReleased:
		var rl Released
		if err := eventlog.Decode(rec.Body, &rl); err != nil {
			return err
		}
		m.ended(rec.At, rl.Intent, true)
	case oracle.KindFinding:
		return m.finding(rec)
	}
	return nil
}

func (m *meter) submitted(at sched.Time, s Submitted) {
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
	var id TxID
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
		var r SubmitResult
		if err := eventlog.Decode(rec.Body, &r); err != nil {
			return err
		}
		m.answers(r)
	case eventlog.KindBlockApplied:
		var b eventlog.BlockApplied
		if err := eventlog.Decode(rec.Body, &b); err != nil {
			return err
		}
		var c BlockContent
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

func (m *meter) answers(r SubmitResult) {
	rejected := 0
	if r.Verdict == VerdictRejected {
		rejected++
	}
	for _, a := range append(again(r), r.Rivals...) {
		if a.Verdict == VerdictRejected {
			rejected++
		}
	}
	m.Rejections += rejected
	if rejected > 0 && m.attacks[r.Intent] {
		m.detected[r.Intent] = true
	}
}

// again is a DoubleSubmit's second answer, if any.
func again(r SubmitResult) []Answer {
	if r.Again == nil {
		return nil
	}
	return []Answer{*r.Again}
}
