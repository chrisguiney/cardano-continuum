package tape

import (
	"testing"

	"github.com/chrisguiney/cardano-continuum/internal/agenda"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/wire"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

func rec(t *testing.T, at sched.Time, kind string, actor uint64, body any) eventlog.Record {
	t.Helper()
	b, err := eventlog.Encode(body)
	if err != nil {
		t.Fatal(err)
	}
	return eventlog.Record{At: at, Class: eventlog.Decision, Kind: kind, Actor: eventlog.ActorID(actor), Body: b}
}

func chose(t *testing.T, at sched.Time, actor uint64, kind string, gain float64) eventlog.Record {
	return rec(t, at, wire.KindIntentChosen, actor, agenda.Decision{
		Intent:   agenda.Intent{Kind: profile.IntentKind(kind)},
		Position: &agenda.Position{Currency: "USD", Gain: gain},
	})
}

// The tape folds quotes, trades, births and decisions: flows by exchange
// and side, births by currency, and in the largest ADA/USD drawdown the
// wallets under water that sold against those that held.
func TestTape(t *testing.T) {
	var tp Tape
	for i, ada := range []float64{1, 1.2, 0.9, 0.6, 0.8, 1.5, 1.3} {
		if err := tp.Add(rec(t, sched.Time(i+1)*60_000, wire.KindQuote, 0, wire.Quote{ADA: ada, Mids: []wire.Mid{{Currency: "USD", Mid: ada}}})); err != nil {
			t.Fatal(err)
		}
	}
	for _, r := range []eventlog.Record{
		rec(t, 1000, wire.KindSpawned, 5, wire.Spawned{Profile: "p", Currency: "EUR"}),
		rec(t, 1000, wire.KindSpawned, 6, wire.Spawned{Profile: "p", Currency: "EUR"}),
		rec(t, 1000, wire.KindSpawned, 7, wire.Spawned{Profile: "pool"}),
		rec(t, 2000, wire.KindTraded, 5, wire.Traded{Side: "buy", Exchange: "north", Currency: "EUR", Lovelace: 10, Fiat: 2}),
		rec(t, 3000, wire.KindTraded, 6, wire.Traded{Side: "sell", Exchange: "north", Currency: "EUR", Lovelace: 4, Fiat: 1}),
		// In the drawdown (peak 1.2 at 120s, trough 0.6 at 240s): 1 and 2
		// are under water, 1 sells, 2 holds; 3 is above water; 4 sells
		// under water only after the trough.
		chose(t, 150_000, 1, "sell_ada", -0.3),
		chose(t, 160_000, 2, "idle", -0.3),
		chose(t, 170_000, 2, "send", -0.35),
		chose(t, 180_000, 3, "sell_ada", 0.2),
		chose(t, 300_000, 4, "sell_ada", -0.3),
	} {
		if err := tp.Add(r); err != nil {
			t.Fatal(err)
		}
	}
	s := tp.Summary()
	if len(s.Quotes) != 7 || s.Quotes[3].ADA != 0.6 {
		t.Fatalf("quotes %+v", s.Quotes)
	}
	if s.Births["EUR"] != 2 || len(s.Births) != 1 {
		t.Fatalf("births %v", s.Births)
	}
	if len(s.Flows) != 2 || s.Flows[0] != (Flow{Exchange: "north", Currency: "EUR", Side: "buy", Trades: 1, Lovelace: 10, Fiat: 2}) {
		t.Fatalf("flows %+v", s.Flows)
	}
	d := s.Drawdown
	if d.Peak != 120_000 || d.Trough != 240_000 || d.Depth != 0.6/1.2-1 {
		t.Fatalf("drawdown %+v", d)
	}
	if d.Sellers != 1 || d.Hodlers != 1 {
		t.Fatalf("sellers %d hodlers %d, want 1 and 1", d.Sellers, d.Hodlers)
	}
}

// Quotes are thinned to a bound by dropping every other one, so a long run
// keeps its whole span.
func TestTapeBoundsQuotes(t *testing.T) {
	var tp Tape
	for i := range 3 * MaxQuotes {
		if err := tp.Add(rec(t, sched.Time(i+1)*1000, wire.KindQuote, 0, wire.Quote{ADA: 1})); err != nil {
			t.Fatal(err)
		}
	}
	s := tp.Summary()
	if len(s.Quotes) > MaxQuotes || s.Quotes[0].At != 1000 || s.Quotes[len(s.Quotes)-1].At < sched.Time(3*MaxQuotes-2)*1000 {
		t.Fatalf("%d quotes from %d to %d", len(s.Quotes), s.Quotes[0].At, s.Quotes[len(s.Quotes)-1].At)
	}
}
