package profile

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
)

func TestHumansAbsentIsImplicit(t *testing.T) {
	f, err := ParseFile(strings.NewReader(marketsHead))
	if err != nil {
		t.Fatal(err)
	}

	if f.Humans != nil {
		t.Fatalf("humans = %+v, want nil without a humans section", f.Humans)
	}
}

func TestHumansEmptyTakesDefaults(t *testing.T) {
	f, err := ParseFile(strings.NewReader(marketsHead + "humans: {}\n"))
	if err != nil {
		t.Fatal(err)
	}

	if f.Humans == nil || !reflect.DeepEqual(*f.Humans, DefaultHumans()) {
		t.Fatalf("humans = %+v, want the defaults", f.Humans)
	}

	d := DefaultHumans()
	if d.Month != 300 || d.MaxWallets != 12 || len(d.Tiers) != 4 {
		t.Fatalf("defaults month %v max %d tiers %d", d.Month, d.MaxWallets, len(d.Tiers))
	}
}

func TestHumansRoundTrip(t *testing.T) {
	in := marketsHead + `humans:
  month: 600
  max_wallets: 4
  spread: beta(1, 1)
  tiers:
    - {name: few, share: 0.75, wallets: 0.5, income: "lognormal(7, 0.2)", expenses: "beta(9, 1)", fiat: "lognormal(5, 1)", layoff: 0.05, hire: 0.2, benefit: 0.25}
    - {name: many, share: 0.25, wallets: 2, income: "lognormal(9, 0.2)", expenses: "beta(5, 5)", fiat: "lognormal(9, 1)", layoff: 0.01, hire: 0.5, benefit: 0.5}
  invest: {share: "beta(2, 8)", every: 0.5, buffer: 2}
`
	f, err := ParseFile(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}

	h := f.Humans
	if h.Month != 600 || h.MaxWallets != 4 || h.Spread.String() != "beta(1, 1)" || len(h.Tiers) != 2 || h.Invest.Every != 0.5 {
		t.Fatalf("humans = %+v", h)
	}

	raw, err := MarshalFile(f)
	if err != nil {
		t.Fatal(err)
	}

	back, err := ParseFile(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("reparse: %v\n%s", err, raw)
	}

	if !reflect.DeepEqual(back.Humans, f.Humans) {
		t.Fatalf("round trip changed humans:\n%+v\n%+v", back.Humans, f.Humans)
	}
}

func TestHumansRejects(t *testing.T) {
	cases := map[string]string{
		"shares":      "tiers: [{name: a, share: 0.5}]",
		"month":       "month: -1",
		"max_wallets": "max_wallets: 65",
		"dist":        "spread: wobble(1)",
		"layoff":      "tiers: [{name: a, share: 1, layoff: 2}]",
		"name":        "tiers: [{name: A, share: 1}]",
		"unknown":     "colour: blue",
		"every":       "invest: {every: -1}",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ParseFile(strings.NewReader(marketsHead + "humans: {" + body + "}\n"))
			if err == nil {
				t.Fatalf("%s accepted", body)
			}

			if !strings.Contains(strings.ToLower(err.Error()), "humans") {
				t.Fatalf("error %q does not name the humans section", err)
			}
		})
	}
}

// A tier that sets only some fields takes the rest from the default tier of
// the same name, or the mid tier for a new name.
func TestHumansTierFill(t *testing.T) {
	f, err := ParseFile(strings.NewReader(marketsHead + "humans: {tiers: [{name: low, share: 0.5, wallets: 1}, {name: rich, share: 0.5}]}\n"))
	if err != nil {
		t.Fatal(err)
	}

	d := DefaultHumans()
	low, rich := f.Humans.Tiers[0], f.Humans.Tiers[1]
	if low.Wallets != 1 || low.Income.String() != d.Tiers[0].Income.String() {
		t.Fatalf("low = %+v", low)
	}

	if rich.Income.String() != d.Tiers[1].Income.String() || rich.Layoff != d.Tiers[1].Layoff {
		t.Fatalf("rich = %+v, want the mid tier's values", rich)
	}
}
