package profile

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	"github.com/chrisguiney/cardano-continuum/profiles"
)

const marketsHead = `version: 1
profiles:
  - name: holder
    kind: wallet
    agenda: hodl
    traits: {competence: "beta(8, 2)", patience: "const(1)", learning_rate: "const(0)", care: "const(1)"}
`

func TestMarketsAbsentIsOff(t *testing.T) {
	f, err := ParseFile(strings.NewReader(marketsHead))
	if err != nil {
		t.Fatal(err)
	}
	if f.Markets != nil {
		t.Fatalf("markets = %+v, want nil without a markets section", f.Markets)
	}
}

func TestMarketsEmptyTakesDefaults(t *testing.T) {
	f, err := ParseFile(strings.NewReader(marketsHead + "markets: {}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if f.Markets == nil {
		t.Fatal("markets nil")
	}
	if !reflect.DeepEqual(*f.Markets, DefaultMarkets()) {
		t.Fatalf("markets = %+v, want the defaults", *f.Markets)
	}
	m := DefaultMarkets()
	if len(m.Currencies) < 2 || len(m.Exchanges) < 2 {
		t.Fatalf("defaults have %d currencies, %d exchanges", len(m.Currencies), len(m.Exchanges))
	}
}

func TestMarketsRoundTrip(t *testing.T) {
	in := marketsHead + `markets:
  step: 5
  ada: {start: 1.5, vol: 0.2}
  currencies:
    - {code: USD, share: 0.6, usd: 1}
    - {code: EUR, share: 0.4, usd: 1.1}
  exchanges:
    - {name: one, spread: 0.01, fee: 100000, float: 1000000000, currencies: [USD, EUR]}
`
	f, err := ParseFile(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	m := f.Markets
	if m.Step != 5 || m.ADA.Start != 1.5 || m.ADA.Vol != 0.2 || m.ADA.JumpRate != DefaultMarkets().ADA.JumpRate {
		t.Fatalf("ada = %+v step %v: explicit values kept, the rest defaulted", m.ADA, m.Step)
	}
	raw, err := MarshalFile(f)
	if err != nil {
		t.Fatal(err)
	}
	back, err := ParseFile(strings.NewReader(string(raw)))
	if err != nil {
		t.Fatalf("reparse: %v\n%s", err, raw)
	}
	if !reflect.DeepEqual(back.Markets, f.Markets) {
		t.Fatalf("round trip:\n got %+v\nwant %+v", back.Markets, f.Markets)
	}
}

func TestMarketsWithoutSectionMarshalsAsBefore(t *testing.T) {
	f, err := ParseFile(strings.NewReader(marketsHead))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := MarshalFile(f)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "markets") {
		t.Fatalf("a file without markets marshals a markets key:\n%s", raw)
	}
}

func TestMarketsRejects(t *testing.T) {
	for name, body := range map[string]string{
		"bad code":         "  currencies: [{code: usd, share: 1, usd: 1}]\n",
		"shares":           "  currencies: [{code: USD, share: 0.5, usd: 1}]\n",
		"unknown currency": "  currencies: [{code: USD, share: 1, usd: 1}]\n  exchanges: [{name: a, spread: 0.01, float: 1, currencies: [EUR]}]\n",
		"spread":           "  currencies: [{code: USD, share: 1, usd: 1}]\n  exchanges: [{name: a, spread: 1.5, float: 1, currencies: [USD]}]\n",
		"no float":         "  currencies: [{code: USD, share: 1, usd: 1}]\n  exchanges: [{name: a, spread: 0.01, currencies: [USD]}]\n",
		"duplicate":        "  currencies: [{code: USD, share: 0.5, usd: 1}, {code: USD, share: 0.5, usd: 1}]\n",
		"extra":            "  extra: 1\n",
		"birth bounds":     "  births: {min: 2, max: 1}\n",
		"negative vol":     "  ada: {vol: -1}\n",
	} {
		if _, err := ParseFile(strings.NewReader(marketsHead + "markets:\n" + body)); err == nil {
			t.Errorf("%s: parsed", name)
		}
	}
}

// The shipped m1 file spells out every market default, so its comments
// document the code's values.
func TestM1MarketsAreTheDefaults(t *testing.T) {
	f, err := ParseFile(bytes.NewReader(profiles.M1))
	if err != nil {
		t.Fatal(err)
	}
	if f.Markets == nil || !reflect.DeepEqual(*f.Markets, DefaultMarkets()) {
		t.Fatalf("m1 markets %+v, want DefaultMarkets", f.Markets)
	}
}
