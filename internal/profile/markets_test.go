package profile

import (
	"bytes"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

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
		"currency vol":     "  currencies: [{code: USD, share: 1, usd: 1, vol: -1}]\n",
		"currency revert":  "  currencies: [{code: USD, share: 1, usd: 1, reversion: -1}]\n",
		"currency drift":   "  currencies: [{code: USD, share: 1, usd: 1, drift: .nan}]\n",
		"drift scale":      "  fx: {drift_scale: -1}\n",
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

func TestMarketHazardsOptIn(t *testing.T) {
	in := strings.Replace(marketsHead, `care: "const(1)"}`, `care: "const(1)"}
    hazards: {dormancy: "exp(1)", market: true}`, 1)
	f, err := ParseFile(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if !f.Profiles[0].Hazards.Market {
		t.Fatal("hazards.market not read")
	}
	raw, err := MarshalFile(f)
	if err != nil {
		t.Fatal(err)
	}
	back, err := ParseFile(bytes.NewReader(raw))
	if err != nil || !back.Profiles[0].Hazards.Market {
		t.Fatalf("hazards.market lost in a round trip: %v\n%s", err, raw)
	}
	d := DefaultMarkets().Deaths
	if !(d.Beta > 0 && d.Window > DefaultMarkets().Births.Window && d.Min > 0 && d.Min <= 1 && d.Max >= 1 && d.Hodl > 0 && d.Hodl < 1) {
		t.Fatalf("death defaults %+v", d)
	}
	if _, err := ParseFile(strings.NewReader(marketsHead + "markets:\n  deaths: {hodl: 2}\n")); err == nil {
		t.Fatal("a hodl exponent above 1 parsed")
	}
}

func TestLossAversionTrait(t *testing.T) {
	in := strings.Replace(marketsHead, `care: "const(1)"}`, `care: "const(1)", loss_aversion: "const(0.8)"}`, 1)
	ps, err := Parse(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if got := ps[0].Traits.Sample(constSource{}).LossAversion; got != 0.8 {
		t.Fatalf("loss aversion %v, want 0.8", got)
	}
	raw, err := Marshal(ps)
	if err != nil {
		t.Fatal(err)
	}
	if back, err := Parse(bytes.NewReader(raw)); err != nil || back[0].Traits.LossAversion.String() != "const(0.8)" {
		t.Fatalf("loss aversion lost in a round trip: %v\n%s", err, raw)
	}
	ps, err = Parse(strings.NewReader(marketsHead))
	if err != nil {
		t.Fatal(err)
	}
	if got := ps[0].Traits.Sample(constSource{}).LossAversion; got != DefaultLossAversion {
		t.Fatalf("unset loss aversion %v, want %v", got, DefaultLossAversion)
	}
}

type constSource struct{}

func (constSource) Float64() float64 { return 0.5 }

// The defaults are seeded from the committed snapshot: ADA/USD and every
// currency's rate and fitted dynamics come from it.
func TestDefaultsComeFromSnapshot(t *testing.T) {
	snap := LoadSnapshot()
	m := DefaultMarkets()
	if m.ADA.Start != snap.ADA.USD {
		t.Fatalf("ada start %v, snapshot %v", m.ADA.Start, snap.ADA.USD)
	}
	if len(m.Currencies) < 12 {
		t.Fatalf("%d default currencies, want at least 12", len(m.Currencies))
	}
	vols := map[float64]bool{}
	for _, c := range m.Currencies {
		s, ok := snap.Currencies[c.Code]
		if !ok || c.USD != s.USD {
			t.Fatalf("%s: usd %v, snapshot %+v", c.Code, c.USD, s)
		}
		if c.Code != "USD" && (c.Vol != s.Vol || c.Drift != s.Drift || c.Reversion != s.Reversion || c.Vol <= 0) {
			t.Fatalf("%s: dynamics %v %v %v, snapshot %+v", c.Code, c.Vol, c.Drift, c.Reversion, s)
		}
		vols[c.Vol] = true
	}
	if len(vols) < len(m.Currencies)/2 {
		t.Fatalf("only %d distinct vols over %d currencies", len(vols), len(m.Currencies))
	}
	if err := m.fill().validate(); err != nil {
		t.Fatal(err)
	}
}

// The snapshot is well formed. This never fetches: it checks the file that
// make market-snapshot wrote.
func TestSnapshotIsWellFormed(t *testing.T) {
	snap := LoadSnapshot()
	if _, err := time.Parse(time.DateOnly, snap.Fetched); err != nil {
		t.Fatalf("fetched %q: %v", snap.Fetched, err)
	}
	if snap.Sources.FX == "" || snap.Sources.ADA == "" {
		t.Fatalf("sources %+v", snap.Sources)
	}
	if !(snap.ADA.USD > 0) {
		t.Fatalf("ada usd %v", snap.ADA.USD)
	}
	for code, c := range snap.Currencies {
		for name, v := range map[string]float64{"usd": c.USD, "vol": c.Vol, "reversion": c.Reversion} {
			if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
				t.Fatalf("%s %s = %v", code, name, v)
			}
		}
		if code != "USD" && (c.USD <= 0 || c.Vol <= 0 || c.Reversion <= 0 || math.IsNaN(c.Drift) || math.IsInf(c.Drift, 0)) {
			t.Fatalf("%s = %+v", code, c)
		}
	}
	if snap.Currencies["USD"].USD != 1 {
		t.Fatal("USD is not 1")
	}
}

// A currency that leaves its dynamics out takes the section's fx values.
func TestFXOfFallsBack(t *testing.T) {
	m := Markets{FX: FXPath{Vol: 0.02, Reversion: 1, DriftScale: 0.5}}
	vol, drift, rev := m.FXOf(Currency{})
	if vol != 0.02 || drift != 0 || rev != 1 {
		t.Fatalf("fallback = %v %v %v", vol, drift, rev)
	}
	vol, drift, rev = m.FXOf(Currency{Vol: 0.3, Drift: -0.2, Reversion: 4})
	if vol != 0.3 || drift != -0.1 || rev != 4 {
		t.Fatalf("own = %v %v %v, want drift scaled by 0.5", vol, drift, rev)
	}
}
