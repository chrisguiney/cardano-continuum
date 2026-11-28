package agenda

import (
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
)

// trading is a world with a market: every actor holds pos.
type trading struct {
	world
	pos Position
}

func (t trading) Position(population.ActorID) (Position, bool) { return t.pos, true }

// tradeAgendas are the wallet agendas that trade on an exchange.
var tradeAgendas = []profile.AgendaName{"hodl", "dapp", "yield", "profit", "opportunist"}

func market(pos Position) trading {
	return trading{world: ok(fakeView{balance: 1000 * ada, stake: true}), pos: pos}
}

// Without a market no agenda scores a trade, not even at zero: a run
// without one decides, and logs its scores, as before.
func TestTradeNeedsAMarket(t *testing.T) {
	for _, name := range tradeAgendas {
		for _, s := range mustFor(t, name).Score(actor(profile.Wallet, 1), ok(fakeView{balance: 1000 * ada})) {
			if s.Kind == profile.IntentBuyADA || s.Kind == profile.IntentSellADA {
				t.Fatalf("%s scores %s without a market", name, s.Kind)
			}
		}
	}
}

// With a market every trading agenda buys and sells now and then, and a
// sale needs ADA to sell.
func TestTradeScores(t *testing.T) {
	for _, name := range tradeAgendas {
		ss := mustFor(t, name).Score(actor(profile.Wallet, 1), market(Position{Currency: "EUR"}))
		if value(ss, profile.IntentBuyADA) <= 0 || value(ss, profile.IntentSellADA) <= 0 {
			t.Fatalf("%s: buy %v sell %v", name, value(ss, profile.IntentBuyADA), value(ss, profile.IntentSellADA))
		}
		can, _ := Intents(name)
		if !slices.Contains(can, profile.IntentBuyADA) || !slices.Contains(can, profile.IntentSellADA) {
			t.Fatalf("%s does not declare its trades: %v", name, can)
		}
	}
	broke := market(Position{Currency: "EUR"})
	broke.balance = 0
	ss := mustFor(t, "hodl").Score(actor(profile.Wallet, 1), broke)
	if value(ss, profile.IntentSellADA) != 0 || value(ss, profile.IntentBuyADA) <= 0 {
		t.Fatal("an empty wallet sells, or cannot buy")
	}
}

// The decision carries the position its scores read, for the why box.
func TestDecideCarriesPosition(t *testing.T) {
	pos := Position{Currency: "JPY", Gain: -0.2, Trend: 0.01}
	d := Decide(actor(profile.Wallet, 1), rand.New(rand.NewPCG(1, 2)), market(pos), mustFor(t, "hodl"), nil)
	if d.Position == nil || *d.Position != pos {
		t.Fatalf("decision position %+v, want %+v", d.Position, pos)
	}
	d = Decide(actor(profile.Wallet, 1), rand.New(rand.NewPCG(1, 2)), ok(fakeView{}), mustFor(t, "hodl"), nil)
	if d.Position != nil {
		t.Fatal("a decision without a market carries a position")
	}
}

func trader(patience, aversion float64) population.Actor {
	a := actor(profile.Wallet, patience)
	a.Traits.LossAversion = aversion
	return a
}

// Under water a loss-averse, impatient wallet sells, more the deeper the
// loss; a patient or calm one sells less; a hodl wallet holds for the
// recovery whatever its traits.
func TestDrawdownSellVsHodl(t *testing.T) {
	down := market(Position{Currency: "USD", Gain: -0.3})
	deeper := market(Position{Currency: "USD", Gain: -0.5})
	flat := market(Position{Currency: "USD"})
	sell := func(name profile.AgendaName, a population.Actor, v View) float64 {
		return value(mustFor(t, name).Score(a, v), profile.IntentSellADA)
	}
	panicky := trader(0.5, 0.9)
	if !(sell("profit", panicky, down) > 5*sell("profit", panicky, flat)) {
		t.Fatalf("panic sale %v against a standing %v", sell("profit", panicky, down), sell("profit", panicky, flat))
	}
	if !(sell("profit", panicky, deeper) > sell("profit", panicky, down)) {
		t.Fatal("a deeper loss does not sell more")
	}
	if !(sell("profit", trader(20, 0.9), down) < sell("profit", panicky, down)) {
		t.Fatal("patience does not hold a sale back")
	}
	if !(sell("profit", trader(0.5, 0.1), down) < sell("profit", panicky, down)) {
		t.Fatal("a calm wallet sells as much as a loss-averse one")
	}
	ss := mustFor(t, "hodl").Score(panicky, down)
	if value(ss, profile.IntentSellADA) != 0 || !strings.Contains(why(ss, profile.IntentSellADA), "hodl") {
		t.Fatalf("hodl under water: sell %v (%q)", value(ss, profile.IntentSellADA), why(ss, profile.IntentSellADA))
	}
}

// On a gain past the take-profit line a wallet takes profit; in a rally it
// buys more, in a slide less.
func TestRallyBuysAndProfitTaking(t *testing.T) {
	a := trader(1, 0.5)
	sellAt := func(g float64) float64 {
		return value(mustFor(t, "hodl").Score(a, market(Position{Gain: g})), profile.IntentSellADA)
	}
	if !(sellAt(0.4) > sellAt(0)) {
		t.Fatal("no profit taking on a 40% gain")
	}
	buyAt := func(trend float64) float64 {
		return value(mustFor(t, "hodl").Score(a, market(Position{Trend: trend})), profile.IntentBuyADA)
	}
	if !(buyAt(0.1) > buyAt(0) && buyAt(0) > buyAt(-0.1)) {
		t.Fatalf("buys %v %v %v do not follow the trend", buyAt(-0.1), buyAt(0), buyAt(0.1))
	}
}

func why(ss []Score, k profile.IntentKind) string {
	for _, s := range ss {
		if s.Kind == k {
			return s.Why
		}
	}
	return ""
}
