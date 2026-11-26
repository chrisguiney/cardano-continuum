package engine

import (
	"cmp"
	"context"
	"fmt"

	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/oracle"
)

// Tally is what a log says about payments.
type Tally struct {
	// Payments and Refills count intents, not attempts: a rebuild of a
	// payment is a second Submitted record for the same intent.
	Payments, Refills int
	Confirmed         int
	// Paid counts the confirmed payments, refills left out.
	Paid     int
	Released int
	// DoublePaid counts confirmations of an intent already confirmed or
	// released. Each is money the engine paid twice or lost track of; a
	// correct run has none, across any number of restarts.
	DoublePaid int
	// Settled counts confirmed intents by kind ("send" for a payment),
	// refills left out.
	Settled map[string]int
	// Guarded counts the stake-floor guard's refusals, Incidents the stall
	// incidents.
	Guarded, Incidents int
	// Rejections counts the classes nodes named in rejections, every send
	// and a DoubleSubmit's second one included.
	Rejections map[string]int
	// Findings counts the oracle's findings, Fatal those of fatal
	// severity; Lost is the lovelace settled payments sent to wrong
	// addresses.
	Findings, Fatal int
	Lost            uint64
}

// Audit folds decision records into a Tally. The zero value is ready.
type Audit struct {
	Tally
	// ended are the intents confirmed or released.
	ended   map[uint64]bool
	seen    map[uint64]bool
	refills map[uint64]bool
	kind    map[uint64]string
}

// Add folds one record. Inputs and other decisions are ignored.
func (a *Audit) Add(rec eventlog.Record) error {
	if a.seen == nil {
		a.seen, a.ended, a.refills = map[uint64]bool{}, map[uint64]bool{}, map[uint64]bool{}
		a.kind, a.Settled = map[uint64]string{}, map[string]int{}
	}
	if rec.Class == eventlog.Input && rec.Kind == eventlog.KindSubmitResult {
		return a.rejections(rec)
	}
	if rec.Class != eventlog.Decision {
		return nil
	}
	switch rec.Kind {
	case oracle.KindFinding:
		f, err := oracle.Decode(rec)
		if err != nil {
			return fmt.Errorf("engine: audit seq %d: %w", rec.Seq, err)
		}
		a.Findings++
		if f.Severity == oracle.Fatal {
			a.Fatal++
		}
		return nil
	case KindGuarded:
		a.Guarded++
		return nil
	case KindIncident:
		a.Incidents++
		return nil
	}
	if rec.Kind == KindSubmitted {
		var s Submitted
		if err := eventlog.Decode(rec.Body, &s); err != nil {
			return fmt.Errorf("engine: audit seq %d: %w", rec.Seq, err)
		}
		a.kind[s.Intent] = cmp.Or(s.Kind, "send")
	}
	switch rec.Kind {
	case KindSubmitted, KindRefill:
		id, err := intentOf(rec)
		if err != nil || a.seen[id] {
			return err
		}
		a.seen[id] = true
		if rec.Kind == KindRefill {
			a.refills[id] = true
			a.Refills++
		} else {
			a.Payments++
		}
	case KindConfirmed:
		var c Confirmed
		if err := eventlog.Decode(rec.Body, &c); err != nil {
			return fmt.Errorf("engine: audit seq %d: %w", rec.Seq, err)
		}
		id := c.Intent
		a.Lost += c.Lost
		a.Confirmed++
		if !a.refills[id] {
			a.Paid++
			if !a.ended[id] {
				a.Settled[a.kind[id]]++
			}
		}
		if a.ended[id] {
			a.DoublePaid++
		}
		a.ended[id] = true
	case KindReleased:
		id, err := intentOf(rec)
		if err != nil {
			return err
		}
		a.Released++
		a.ended[id] = true
	}
	return nil
}

func (a *Audit) rejections(rec eventlog.Record) error {
	var r SubmitResult
	if err := eventlog.Decode(rec.Body, &r); err != nil {
		return fmt.Errorf("engine: audit seq %d: %w", rec.Seq, err)
	}
	var cs []string
	if r.Verdict == VerdictRejected {
		cs = append(cs, r.Failures...)
	}
	if r.Again != nil && r.Again.Verdict == VerdictRejected {
		cs = append(cs, r.Again.Failures...)
	}
	for _, c := range cs {
		if a.Rejections == nil {
			a.Rejections = map[string]int{}
		}
		a.Rejections[c]++
	}
	return nil
}

// intentOf reads the intent a payment decision names. Bodies are decoded
// into their own types: the log's decoder refuses unknown fields.
func intentOf(rec eventlog.Record) (uint64, error) {
	var id uint64
	var err error
	switch rec.Kind {
	case KindSubmitted:
		id, err = decodeIntent(rec.Body, func(b Submitted) uint64 { return b.Intent })
	case KindRefill:
		id, err = decodeIntent(rec.Body, func(b Refill) uint64 { return b.Intent })
	case KindConfirmed:
		id, err = decodeIntent(rec.Body, func(b Confirmed) uint64 { return b.Intent })
	case KindReleased:
		id, err = decodeIntent(rec.Body, func(b Released) uint64 { return b.Intent })
	}
	if err != nil {
		return 0, fmt.Errorf("engine: audit seq %d %s: %w", rec.Seq, rec.Kind, err)
	}
	return id, nil
}

func decodeIntent[T any](body []byte, intent func(T) uint64) (uint64, error) {
	var b T
	if err := eventlog.Decode(body, &b); err != nil {
		return 0, err
	}
	return intent(b), nil
}

// AuditLog tallies a whole log.
func AuditLog(ctx context.Context, r eventlog.Reader) (Tally, error) {
	var a Audit
	if err := r.Scan(ctx, 1, a.Add); err != nil {
		return Tally{}, err
	}
	return a.Tally, nil
}
