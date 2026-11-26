package engine

import (
	"fmt"
	"math"
	"slices"
	"strings"
	"testing"

	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
)

// amountProfileYAML is the busy test population with a payment-size
// distribution far below the default's balance fractions: payments of about
// 20 ADA from wallets holding 500,000 ADA.
var amountProfileYAML = strings.Replace(
	strings.Replace(testProfileYAML, "budget: 200000000", "budget: 500000000000", 1),
	"    hazards:", "    amount:\n      body: lognormal(16.8, 0.4)\n    hazards:", 1)

func amountConfig(t *testing.T, yaml string, seed uint64) Config {
	t.Helper()
	cfg := testConfig(t, seed, 40)
	ps, err := profile.Parse(strings.NewReader(yaml))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Profiles, err = profile.NewRegistry(ps); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func submittedAmounts(t *testing.T, cfg Config) []float64 {
	t.Helper()
	w := newWorld(t, cfg)
	w.run(600_000, 25, nil)
	var xs []float64
	for _, s := range readPayments(t, w.log).submitted {
		xs = append(xs, float64(s.Amount))
	}
	slices.Sort(xs)
	return xs
}

// Payment sizes follow the profile's amount distribution when it has one.
func TestPaymentSizesFollowAmount(t *testing.T) {
	xs := submittedAmounts(t, amountConfig(t, amountProfileYAML, 5))
	if len(xs) < 100 {
		t.Fatalf("only %d payments", len(xs))
	}
	want := math.Exp(16.8)
	if got := xs[len(xs)/2]; math.Abs(math.Log(got/want)) > 0.15 {
		t.Errorf("median payment %.0f, want about %.0f", got, want)
	}
	if hi := xs[len(xs)-1]; hi > math.Exp(16.8+4*0.4+0.5) {
		t.Errorf("largest payment %.0f is far past the body", hi)
	}
}

// A profile without the field keeps the balance-fraction sizing: payments of
// at least 5% of a 200 ADA balance.
func TestPaymentSizesDefaultWithoutAmount(t *testing.T) {
	xs := submittedAmounts(t, testConfig(t, 5, 40))
	if len(xs) == 0 {
		t.Fatal("no payments")
	}
	if xs[0] < minPayment {
		t.Errorf("smallest payment %.0f below the minimum", xs[0])
	}
}

// A drawn size above the balance is capped to what the wallet can pay, so a
// heavy tail never wedges a wallet or breaks conservation. Every draw here
// is far above any balance: uncapped, each payment would be skipped as
// unaffordable although the wallet could pay the minimum.
func TestPaymentSizeCappedToBalance(t *testing.T) {
	y := strings.Replace(amountProfileYAML, "lognormal(16.8, 0.4)", "lognormal(30, 0.1)", 1)
	cfg := amountConfig(t, y, 6)
	w := newWorld(t, cfg)
	w.run(600_000, 25, nil)
	if len(readPayments(t, w.log).submitted) == 0 {
		t.Fatal("no payments: an oversize draw must be capped, not skipped")
	}
	for _, rec := range logRecords(t, w.log) {
		if rec.Kind != KindSkipped {
			continue
		}
		var s Skipped
		if err := eventlog.Decode(rec.Body, &s); err != nil {
			t.Fatal(err)
		}
		var have, need uint64
		if _, err := fmt.Sscanf(s.Why, "spendable %d below %d", &have, &need); err == nil && have >= minPayment+feeHeadroom {
			t.Fatalf("actor %d skipped a payment of %d holding %d: the draw was not capped", rec.Actor, need-feeHeadroom, have)
		}
	}
}
