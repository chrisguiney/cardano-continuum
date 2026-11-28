package tape

import (
	"cmp"
	"fmt"
	"maps"
	"slices"

	"github.com/chrisguiney/cardano-continuum/internal/agenda"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/wire"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

// Bounds on what a tape keeps. Quotes are thinned past MaxQuotes; under-
// water decisions past maxMarks are not kept, and the split then counts
// only the first ones.
const (
	MaxQuotes = 2000
	maxMarks  = 1 << 20
)

// Point is one Quote record: ADA/USD and each currency's mid.
type Point struct {
	At   sched.Time         `json:"at"`
	ADA  float64            `json:"ada"`
	Mids map[string]float64 `json:"mids"`
	// Venues are each exchange's quotes, "exchange/currency" to bid and ask.
	Venues map[string][2]float64 `json:"venues"`
}

// Flow is the settled trades of one exchange, currency and side.
type Flow struct {
	Exchange string  `json:"exchange"`
	Currency string  `json:"currency"`
	Side     string  `json:"side"`
	Trades   int     `json:"trades"`
	Lovelace uint64  `json:"lovelace"`
	Fiat     float64 `json:"fiat"`
}

// Drawdown is the largest fall of ADA/USD from a peak to a later trough,
// and the split of the wallets that decided under water in it: Sellers
// chose sell_ada at least once, Hodlers never did.
type Drawdown struct {
	Peak    sched.Time `json:"peak_at"`
	Trough  sched.Time `json:"trough_at"`
	High    float64    `json:"high"`
	Low     float64    `json:"low"`
	Depth   float64    `json:"depth"`
	Hodlers int        `json:"hodlers"`
	Sellers int        `json:"sellers"`
}

// Summary is what a tape shows.
type Summary struct {
	Quotes   []Point        `json:"quotes"`
	Flows    []Flow         `json:"flows"`
	Births   map[string]int `json:"births"`
	Drawdown Drawdown       `json:"drawdown"`
}

// mark is one decision a wallet made under water.
type mark struct {
	// at is the decision's time.
	at sched.Time
	// wallet is the deciding actor.
	wallet uint64
	// sold is a sell_ada choice.
	sold bool
}

type flowKey struct{ exchange, currency, side string }

// Tape is the fold. The zero value is empty and ready.
type Tape struct {
	// quotes is the thinned series and stride the quotes per kept one.
	quotes []Point
	stride int
	// seen counts the quotes since the last kept one.
	seen int
	// peak is the running ADA/USD high and its time; dd the deepest fall.
	peak   Point
	dd     Drawdown
	flows  map[flowKey]*Flow
	births map[string]int
	marks  []mark
}

// Add folds one record; records of other kinds are ignored.
func (t *Tape) Add(r eventlog.Record) error {
	var err error
	switch r.Kind {
	case wire.KindQuote:
		var q wire.Quote
		if err = eventlog.Decode(r.Body, &q); err == nil {
			t.quote(r.At, q)
		}
	case wire.KindTraded:
		var tr wire.Traded
		if err = eventlog.Decode(r.Body, &tr); err == nil {
			t.trade(tr)
		}
	case wire.KindSpawned:
		var s wire.Spawned
		if err = eventlog.Decode(r.Body, &s); err == nil && s.Currency != "" {
			if t.births == nil {
				t.births = map[string]int{}
			}
			t.births[s.Currency]++
		}
	case wire.KindIntentChosen:
		var d agenda.Decision
		if err = eventlog.Decode(r.Body, &d); err == nil {
			t.decide(r.At, uint64(r.Actor), d)
		}
	}
	if err != nil {
		return fmt.Errorf("tape: seq %d %s: %w", r.Seq, r.Kind, err)
	}
	return nil
}

func (t *Tape) quote(at sched.Time, q wire.Quote) {
	p := Point{At: at, ADA: q.ADA, Mids: map[string]float64{}, Venues: map[string][2]float64{}}
	for _, m := range q.Mids {
		p.Mids[m.Currency] = m.Mid
	}
	for _, v := range q.Venues {
		p.Venues[v.Exchange+"/"+v.Currency] = [2]float64{v.Bid, v.Ask}
	}

	if p.ADA > t.peak.ADA {
		t.peak = p
	}
	if depth := p.ADA/t.peak.ADA - 1; depth < t.dd.Depth {
		t.dd = Drawdown{Peak: t.peak.At, Trough: at, High: t.peak.ADA, Low: p.ADA, Depth: depth}
	}

	t.stride = max(t.stride, 1)
	if t.seen++; t.seen < t.stride && len(t.quotes) > 0 {
		return
	}
	t.seen = 0
	t.quotes = append(t.quotes, p)
	if len(t.quotes) >= MaxQuotes {
		kept := t.quotes[:0]
		for i := 0; i < len(t.quotes); i += 2 {
			kept = append(kept, t.quotes[i])
		}
		t.quotes, t.stride = kept, t.stride*2
	}
}

func (t *Tape) trade(tr wire.Traded) {
	if t.flows == nil {
		t.flows = map[flowKey]*Flow{}
	}
	k := flowKey{tr.Exchange, tr.Currency, tr.Side}
	f := t.flows[k]
	if f == nil {
		f = &Flow{Exchange: tr.Exchange, Currency: tr.Currency, Side: tr.Side}
		t.flows[k] = f
	}
	f.Trades++
	f.Lovelace += tr.Lovelace
	f.Fiat += tr.Fiat
}

func (t *Tape) decide(at sched.Time, wallet uint64, d agenda.Decision) {
	if d.Position == nil || d.Position.Gain > -agenda.LossGain || len(t.marks) >= maxMarks {
		return
	}
	t.marks = append(t.marks, mark{at: at, wallet: wallet, sold: d.Intent.Kind == profile.IntentSellADA})
}

// Summary is the fold so far.
func (t *Tape) Summary() Summary {
	s := Summary{Quotes: slices.Clone(t.quotes), Births: maps.Clone(t.births), Drawdown: t.dd}
	if s.Births == nil {
		s.Births = map[string]int{}
	}
	for _, f := range t.flows {
		s.Flows = append(s.Flows, *f)
	}
	slices.SortFunc(s.Flows, func(a, b Flow) int {
		return cmp.Or(cmp.Compare(a.Exchange, b.Exchange), cmp.Compare(a.Currency, b.Currency), cmp.Compare(a.Side, b.Side))
	})

	sold := map[uint64]bool{}
	for _, m := range t.marks {
		if m.at >= t.dd.Peak && m.at <= t.dd.Trough && t.dd.Depth < 0 {
			sold[m.wallet] = sold[m.wallet] || m.sold
		}
	}
	for _, v := range sold {
		if v {
			s.Drawdown.Sellers++
		} else {
			s.Drawdown.Hodlers++
		}
	}
	return s
}
