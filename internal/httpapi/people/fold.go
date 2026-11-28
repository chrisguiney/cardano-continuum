package people

import (
	"slices"

	"github.com/chrisguiney/cardano-continuum/internal/engine"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
)

// BinMs is the width of a Flow bucket: five minutes of simulated time, the
// default month, so a bucket is about one payday per human.
const BinMs = 300_000

// maxEvents bounds the money events kept per human.
const maxEvents = 64

// Event is one entry of a human's money timeline. Cents are of its
// currency; Wallet is the wallet a purchase went to, zero otherwise.
type Event struct {
	Seq    uint64 `json:"seq"`
	At     int64  `json:"at"`
	Kind   string `json:"kind"`
	Cents  int64  `json:"cents"`
	Detail string `json:"detail,omitempty"`
	Wallet uint64 `json:"wallet,omitempty"`
}

// Flow is one currency's fiat flows in one bucket of BinMs, in cents:
// pay received, bills paid, purchases from income (Invested), purchases
// the wallets' own agendas chose (Scored), and sales. Paydays and Out count
// paydays and those out of work.
type Flow struct {
	Currency string `json:"currency"`
	At       int64  `json:"at"`
	Income   int64  `json:"income"`
	Bills    int64  `json:"bills"`
	Invested int64  `json:"invested"`
	Scored   int64  `json:"scored"`
	Sold     int64  `json:"sold"`
	Paydays  int    `json:"paydays"`
	Out      int    `json:"out"`
}

// tally is what the log says of one human.
type tally struct {
	currency                      string
	months, out, short, purchases int
	income, paid, invested        int64
	bought, sold                  int64
	employed, left                bool
	events                        []Event
}

type binKey struct {
	currency string
	bin      int64
}

// Fold folds a log's human records. The zero value is ready.
type Fold struct {
	humans map[uint64]*tally
	flows  map[binKey]*Flow
	// dca are the intents of purchases from income, so their Traded
	// record counts as invested rather than scored.
	dca map[uint64]bool
}

func (f *Fold) human(id uint64) *tally {
	if f.humans == nil {
		f.humans = map[uint64]*tally{}
	}

	t := f.humans[id]
	if t == nil {
		t = &tally{employed: true}
		f.humans[id] = t
	}
	return t
}

// get is what the log says of a human, the zero tally when nothing.
func (f *Fold) get(id uint64) tally {
	if t := f.humans[id]; t != nil {
		return *t
	}
	return tally{employed: true}
}

func (f *Fold) flow(currency string, at int64) *Flow {
	if f.flows == nil {
		f.flows = map[binKey]*Flow{}
	}

	k := binKey{currency, at / BinMs}
	fl := f.flows[k]
	if fl == nil {
		fl = &Flow{Currency: currency, At: k.bin * BinMs}
		f.flows[k] = fl
	}
	return fl
}

func (t *tally) note(e Event) {
	t.events = append(t.events, e)
	if len(t.events) > maxEvents {
		t.events = slices.Delete(t.events, 0, len(t.events)-maxEvents)
	}
}

// Add folds one record; records of other kinds are ignored.
func (f *Fold) Add(r eventlog.Record) error {
	if r.Class != eventlog.Decision {
		return nil
	}

	switch r.Kind {
	case engine.KindHumanBorn:
		var b engine.HumanBorn
		if err := eventlog.Decode(r.Body, &b); err != nil {
			return err
		}
		f.human(b.Human).currency = b.Currency
	case engine.KindHumanLeft:
		var b engine.HumanLeft
		if err := eventlog.Decode(r.Body, &b); err != nil {
			return err
		}
		t := f.human(b.Human)
		t.left = true
		t.note(Event{Seq: r.Seq, At: int64(r.At), Kind: r.Kind, Detail: b.Cause})
	case engine.KindIncome:
		return f.income(r)
	case engine.KindExpense:
		return f.expense(r)
	case engine.KindEmployment:
		var b engine.Employment
		if err := eventlog.Decode(r.Body, &b); err != nil {
			return err
		}
		detail := "lost the job"
		if b.Employed {
			detail = "found a job"
		}
		f.human(b.Human).note(Event{Seq: r.Seq, At: int64(r.At), Kind: r.Kind, Detail: detail})
	case engine.KindInvested:
		return f.invested(r)
	case engine.KindTraded:
		return f.traded(r)
	}
	return nil
}

func (f *Fold) income(r eventlog.Record) error {
	var b engine.Income
	if err := eventlog.Decode(r.Body, &b); err != nil {
		return err
	}

	t := f.human(b.Human)
	t.currency, t.employed = b.Currency, b.Employed
	t.months++
	t.income += b.Cents
	fl := f.flow(b.Currency, int64(r.At))
	fl.Income += b.Cents
	fl.Paydays++
	detail := "pay"
	if !b.Employed {
		t.out++
		fl.Out++
		detail = "benefit, out of work"
	}
	t.note(Event{Seq: r.Seq, At: int64(r.At), Kind: r.Kind, Cents: b.Cents, Detail: detail})
	return nil
}

func (f *Fold) expense(r eventlog.Record) error {
	var b engine.Expense
	if err := eventlog.Decode(r.Body, &b); err != nil {
		return err
	}

	t := f.human(b.Human)
	t.paid += b.Paid
	f.flow(t.currency, int64(r.At)).Bills += b.Paid
	detail := "bills paid"
	if b.Short > 0 {
		t.short++
		detail = "bills short: a forced sale of ADA follows"
	}
	t.note(Event{Seq: r.Seq, At: int64(r.At), Kind: r.Kind, Cents: -b.Paid, Detail: detail})
	return nil
}

func (f *Fold) invested(r eventlog.Record) error {
	var b engine.Invested
	if err := eventlog.Decode(r.Body, &b); err != nil {
		return err
	}

	if f.dca == nil {
		f.dca = map[uint64]bool{}
	}
	f.dca[b.Intent] = true
	t := f.human(b.Human)
	t.purchases++
	t.note(Event{Seq: r.Seq, At: int64(r.At), Kind: r.Kind, Cents: b.Cents, Detail: b.Style, Wallet: b.Wallet})
	return nil
}

func (f *Fold) traded(r eventlog.Record) error {
	var b engine.Traded
	if err := eventlog.Decode(r.Body, &b); err != nil {
		return err
	}

	if b.Human == 0 {
		return nil
	}

	// A trade can settle before a genesis human's first payday names its
	// currency; it counts for the human but in no currency's flows.
	t := f.human(b.Human)
	fl := &Flow{}
	if t.currency != "" {
		fl = f.flow(t.currency, int64(r.At))
	}

	switch {
	case b.Side != "buy":
		t.sold += b.Cents
		fl.Sold += b.Cents
	case f.dca[b.Intent]:
		t.bought += b.Cents
		t.invested += b.Cents
		fl.Invested += b.Cents
		delete(f.dca, b.Intent)
	default:
		t.bought += b.Cents
		fl.Scored += b.Cents
	}
	return nil
}

// Flows are every currency's flows, by currency then time.
func (f *Fold) Flows() []Flow {
	out := make([]Flow, 0, len(f.flows))
	for _, fl := range f.flows {
		out = append(out, *fl)
	}

	slices.SortFunc(out, func(a, b Flow) int {
		if a.Currency != b.Currency {
			if a.Currency < b.Currency {
				return -1
			}
			return 1
		}
		return int(a.At - b.At)
	})
	return out
}
