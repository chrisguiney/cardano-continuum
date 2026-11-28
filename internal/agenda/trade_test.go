package agenda

import (
	"math/rand/v2"
	"slices"
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
