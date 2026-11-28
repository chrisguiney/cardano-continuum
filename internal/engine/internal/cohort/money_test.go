package cohort

import (
	"iter"
	"slices"
	"testing"

	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/wire"
	"github.com/chrisguiney/cardano-continuum/internal/household"
	"github.com/chrisguiney/cardano-continuum/internal/market"
	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
	"github.com/chrisguiney/cardano-continuum/internal/rng"
)

// flight is a fixed set of unfinished intents.
type flight []*wire.Pending

func (f flight) All() iter.Seq[*wire.Pending] { return slices.Values(f) }
func (f flight) Spendable(uint64) uint64      { return 0 }

// moneyFixture is one human with one wallet (actor 1), fiat cents and a
// month's bills of bills, the open orders in f.
func moneyFixture(fiat, bills int64, f flight) (*Money, *Person) {
	spec := profile.DefaultHumans()
	mk := profile.DefaultMarkets()
	pp := testPeople(&spec)
	pp.m = &Market{path: market.New(rng.Seed{1}, mk), r: pp.r}
	pp.r.census.SpawnOwned(wallet("w", "hodl"), pp.r.seed, 0, population.Owner{Human: 1, Purpose: population.Spending})
	h := &Person{
		ID:       1,
		Currency: 0,
		Trader:   &Trader{},
		Purse: &Purse{
			Budget: household.Budget{Buffer: 1, Steps: 4},
			State:  household.State{Bills: bills},
			Fiat:   fiat,
		},
	}
	pp.list = []*Person{h}
	return newMoney(pp, f), h
}

func buy(amount, fee uint64, price float64, cents int64) *wire.Pending {
	return &wire.Pending{
		Actor: 9,
		Args:  wire.Args{Kind: string(profile.IntentBuyADA), To: 1, Amount: amount},
		Order: &wire.Order{Wallet: 1, Primary: price, Fee: fee, Human: 1, Cents: cents},
	}
}

// What open buys hold is their orders' Cents, nothing else.
func TestHeldIsTheOpenBuys(t *testing.T) {
	t.Parallel()
	sell := buy(1, 0, 1, 99)
	sell.Kind = string(profile.IntentSellADA)
	other := buy(1, 0, 1, 7)
	other.Order.Human = 2
	mo, h := moneyFixture(1000, 0, flight{buy(1, 0, 1, 300), sell, other})
	if got := mo.Held(1); got != 300 {
		t.Fatalf("held %d, want the one open buy's 300", got)
	}

	if got := mo.Available(h); got != 700 {
		t.Fatalf("available %d, want 700", got)
	}
}

// A scored buy holds what it costs; one that would spend into the buffer
// shrinks to what is above it, and one below the minimum is refused.
func TestFundBoundsABuyByFiat(t *testing.T) {
	t.Parallel()
	mo, _ := moneyFixture(10_000, 2_000, nil)
	p := buy(99_000_000, 1_000_000, 0.5, 0)
	p.Order.Human, p.Order.Cents = 0, 0
	if why := mo.Fund(p, 5_000_000); why != "" || p.Order.Cents != 5_000 || p.Amount != 99_000_000 {
		t.Fatalf("fund %q: amount %d cents %d, want 100 ADA holding 5000", why, p.Amount, p.Order.Cents)
	}

	mo, _ = moneyFixture(4_000, 2_000, nil)
	p = buy(99_000_000, 1_000_000, 0.5, 0)
	if why := mo.Fund(p, 5_000_000); why != "" || p.Order.Cents != 2_000 || p.Amount != 39_000_000 {
		t.Fatalf("fund %q: amount %d cents %d, want 40 ADA holding the 2000 above the buffer", why, p.Amount, p.Order.Cents)
	}

	mo, _ = moneyFixture(2_100, 2_000, nil)
	if why := mo.Fund(buy(99_000_000, 1_000_000, 0.5, 0), 5_000_000); why != "no fiat above the buffer" {
		t.Fatalf("fund %q, want refused", why)
	}
}

// Settling a buy spends exactly its hold; a sale adds its proceeds,
// rounded down.
func TestSettledMovesFiat(t *testing.T) {
	t.Parallel()
	mo, h := moneyFixture(10_000, 0, nil)
	if who, cents := mo.Settled(buy(10_000_000, 0, 0.5, 501)); who != 1 || cents != 501 || h.Purse.Fiat != 9_499 {
		t.Fatalf("buy settled for %d cents %d, fiat %d", who, cents, h.Purse.Fiat)
	}

	sell := buy(3_333_333, 0, 0.5, 0)
	sell.Kind = string(profile.IntentSellADA)
	if _, cents := mo.Settled(sell); cents != 166 || h.Purse.Fiat != 9_665 {
		t.Fatalf("sale brought %d cents, fiat %d", cents, h.Purse.Fiat)
	}
}

// Without humans the money does nothing: a buy goes ahead untouched.
func TestMoneyOffIsInert(t *testing.T) {
	t.Parallel()
	mo := newMoney(testPeople(nil), nil)
	p := buy(10_000_000, 0, 0.5, 0)
	p.Order.Human = 0
	if why := mo.Fund(p, 1); why != "" || p.Order.Cents != 0 || p.Order.Human != 0 {
		t.Fatalf("fund without humans: %q %+v", why, p.Order)
	}

	if who, _ := mo.Settled(p); who != 0 {
		t.Fatal("a settled trade without humans moved fiat")
	}
}
