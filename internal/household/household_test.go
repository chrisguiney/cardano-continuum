package household

import (
	"math"
	"math/rand/v2"
	"testing"

	"github.com/chrisguiney/cardano-continuum/internal/profile"
)

func stream(n uint64) *rand.Rand { return rand.New(rand.NewPCG(n, n)) }

func budget() Budget {
	return Budget{
		Income:  300_000,
		Ratio:   0.8,
		Benefit: 0.3,
		Layoff:  0.02,
		Hire:    0.3,
		Share:   0.2,
		Steps:   4,
		Buffer:  1,
		Style:   DCA,
	}
}

func TestDrawConvertsUSDToCents(t *testing.T) {
	t.Parallel()
	d := profile.DefaultHumans()
	tier := d.Tiers[1]
	b, fiat := Draw(tier, d.Invest, "hodl", 0.5, stream(1))
	if b.Income <= 0 || fiat <= 0 || b.Ratio <= 0 || b.Ratio >= 1 || b.Share <= 0 || b.Share >= 1 {
		t.Fatalf("budget %+v fiat %d", b, fiat)
	}

	again, fiatAgain := Draw(tier, d.Invest, "hodl", 0.5, stream(1))
	if again != b || fiatAgain != fiat {
		t.Fatal("the same stream draws a different budget")
	}

	// Half a USD per unit: the same dollar income is twice the units.
	usd, _ := Draw(tier, d.Invest, "hodl", 1, stream(1))
	if math.Abs(float64(b.Income)/float64(usd.Income)-2) > 0.01 {
		t.Fatalf("income %d at 0.5 USD/unit vs %d at 1", b.Income, usd.Income)
	}

	if b.Steps != 4 || b.Style != DCA {
		t.Fatalf("steps %d style %s, want 4 dca", b.Steps, b.Style)
	}

	if p, _ := Draw(tier, d.Invest, "profit", 1, stream(1)); p.Style != Momentum {
		t.Fatalf("profit style %s, want momentum", p.Style)
	}
}

// A month pays the income (the benefit share when unemployed) and the
// bills from what is available, never below zero: the shortfall is
// reported, not borrowed.
func TestMonthPaysAndFloorsAtZero(t *testing.T) {
	t.Parallel()
	b := budget()
	b.Layoff, b.Hire = 0, 0
	m := Month(b, State{Employed: true}, 0, stream(2))
	if !m.Employed || m.Income <= 0 || m.Due <= 0 || m.Paid != m.Due || m.Short != 0 {
		t.Fatalf("month %+v", m)
	}

	if m.Disposable != max(m.Income-m.Due, 0) {
		t.Fatalf("disposable %d of income %d less bills %d", m.Disposable, m.Income, m.Due)
	}

	broke := Month(b, State{Employed: false}, -1_000_000, stream(2))
	if broke.Employed || broke.Paid != 0 || broke.Short != broke.Due {
		t.Fatalf("an unemployed human with nothing available: %+v", broke)
	}

	if want := int64(float64(b.Income) * b.Benefit); math.Abs(float64(broke.Income-want)) > float64(want)/5 {
		t.Fatalf("benefit income %d, want about %d", broke.Income, want)
	}
}

// Employment is a two-state chain: with layoff 1 an employed human loses
// its job, with hire 1 an unemployed one finds one.
func TestMonthEmployment(t *testing.T) {
	t.Parallel()
	b := budget()
	b.Layoff, b.Hire = 1, 1
	if Month(b, State{Employed: true}, 0, stream(3)).Employed {
		t.Fatal("layoff 1 kept the job")
	}

	if !Month(b, State{Employed: false}, 0, stream(3)).Employed {
		t.Fatal("hire 1 found no job")
	}
}

// A tranche is the share of last month's disposable income spread over the
// month's steps, scaled by the style, and never spends the buffer.
func TestTranche(t *testing.T) {
	t.Parallel()
	b := budget()
	s := State{Disposable: 100_000, Bills: 200_000}
	if got := Tranche(b, s, 10_000_000, 0); got != 5_000 {
		t.Fatalf("dca tranche %d, want 0.2 * 100000 / 4 = 5000", got)
	}

	if got := Tranche(b, s, 203_000, 0); got != 3_000 {
		t.Fatalf("tranche %d, want what is above the buffer, 3000", got)
	}

	if got := Tranche(b, s, 150_000, 0); got != 0 {
		t.Fatalf("tranche %d inside the buffer, want 0", got)
	}

	b.Style = Dip
	if fall, rise := Tranche(b, s, 10_000_000, -0.2), Tranche(b, s, 10_000_000, 0.2); fall <= 5_000 || rise >= 5_000 {
		t.Fatalf("dip buys %d in a fall and %d in a rise", fall, rise)
	}

	b.Style = Momentum
	if fall, rise := Tranche(b, s, 10_000_000, -0.2), Tranche(b, s, 10_000_000, 0.2); fall >= 5_000 || rise <= 5_000 {
		t.Fatalf("momentum buys %d in a fall and %d in a rise", fall, rise)
	}
}

// Cents of an amount of ADA at a price: a hold rounds up, a sale's proceeds
// round down, so rounding never creates fiat.
func TestCentsRounding(t *testing.T) {
	t.Parallel()
	if got := Hold(1_000_001, 0.5); got != 51 {
		t.Fatalf("Hold = %d, want 51", got)
	}

	if got := Proceeds(1_000_001, 0.5); got != 50 {
		t.Fatalf("Proceeds = %d, want 50", got)
	}

	if got := Lovelace(5_000, 0.25); got != 200_000_000 {
		t.Fatalf("Lovelace = %d, want 200 ADA", got)
	}
}
