package engine

import (
	"math"
	"strings"
	"testing"

	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
)

// A profile's Hawkes parameters keep its actors' payment rate: the base
// rate is cut to mu = rate * (1 - alpha/beta), and each payment begets
// alpha/beta follow-ups on average, so the total is what the profile asks
// for. Wake-ups drop, since follow-ups do not idle.
func TestHawkesKeepsThePaymentRate(t *testing.T) {
	hodl := strings.NewReplacer("      diurnal: 0.3\n", "", "    weights:\n      send: 0.9\n      delegate: 0.1\n", "").Replace(testProfileYAML)
	burst := strings.Replace(hodl, "        delegate: 0.1\n", "        delegate: 0.1\n      hawkes: {alpha: 0.6, beta: 1}\n", 1)
	_, n0 := shortTxGaps(t, hodl)
	_, n1 := shortTxGaps(t, burst)
	if math.Abs(math.Log(float64(n1)/float64(n0))) > 0.35 {
		t.Fatalf("%d payment gaps with excitation, %d without: the rate moved", n1, n0)
	}
}

// reuse is the share of payments whose payee the paying actor paid before.
func reuse(t *testing.T, care string) float64 {
	t.Helper()
	y := strings.Replace(testProfileYAML, "care: beta(2, 2)", "care: "+care, 1)
	cfg := testConfig(t, 9, 30)
	ps, err := profile.Parse(strings.NewReader(y))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Profiles, err = profile.NewRegistry(ps); err != nil {
		t.Fatal(err)
	}
	w := newWorld(t, cfg)
	w.run(600_000, 50, nil)
	pays := readPayments(t, w.log)
	paid := map[[2]uint64]bool{}
	seen := map[uint64]bool{}
	var n, again float64
	for _, s := range pays.submitted {
		if s.Kind != "" || seen[s.Intent] {
			continue
		}
		seen[s.Intent] = true
		k := [2]uint64{pays.actor[s.Intent], s.To}
		n++
		if paid[k] {
			again++
		}
		paid[k] = true
	}
	return again / n
}

// Payee choice follows the care trait, the fitted share of a unit's
// outputs that go to addresses it paid before: a careful actor pays its
// known counterparties again, a careless one anybody.
func TestCarePrefersKnownPayees(t *testing.T) {
	lo, hi := reuse(t, "const(0)"), reuse(t, "const(0.9)")
	t.Logf("reuse at care 0: %.2f, at care 0.9: %.2f", lo, hi)
	if hi < lo+0.3 || hi < 0.6 {
		t.Fatalf("reuse %.2f at care 0.9 against %.2f at care 0", hi, lo)
	}
}

// shortTxGaps is the share of gaps between an actor's consecutive
// payments that are under two seconds, with the number of gaps.
func shortTxGaps(t *testing.T, yaml string) (float64, int) {
	t.Helper()
	cfg := testConfig(t, 9, 30)
	ps, err := profile.Parse(strings.NewReader(yaml))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Profiles, err = profile.NewRegistry(ps); err != nil {
		t.Fatal(err)
	}
	w := newWorld(t, cfg)
	w.run(1_200_000, 50, nil)
	last := map[uint64]int64{}
	seen := map[uint64]bool{}
	var n, short float64
	for _, rec := range logRecords(t, w.log) {
		if rec.Kind != KindSubmitted {
			continue
		}
		var s Submitted
		if err := eventlog.Decode(rec.Body, &s); err != nil {
			t.Fatal(err)
		}
		if seen[s.Intent] {
			continue
		}
		seen[s.Intent] = true
		if prev, ok := last[uint64(rec.Actor)]; ok {
			n++
			if int64(rec.At)-prev < 2_000 {
				short++
			}
		}
		last[uint64(rec.Actor)] = int64(rec.At)
	}
	return short / n, int(n)
}

// Excitation bursts transactions, not idle wake-ups: an excited wake-up is
// a follow-up and acts, so with a branching ratio of 0.6 and bursts of a
// second, most of a hodler's payments come seconds after its last.
func TestHawkesBurstsTransactions(t *testing.T) {
	hodl := strings.NewReplacer("      diurnal: 0.3\n", "", "    weights:\n      send: 0.9\n      delegate: 0.1\n", "").Replace(testProfileYAML)
	burst := strings.Replace(hodl, "        delegate: 0.1\n", "        delegate: 0.1\n      hawkes: {alpha: 0.6, beta: 1}\n", 1)
	calm, n0 := shortTxGaps(t, hodl)
	bursty, n1 := shortTxGaps(t, burst)
	t.Logf("short gaps: calm %.2f of %d, bursty %.2f of %d", calm, n0, bursty, n1)
	if bursty < calm+0.3 {
		t.Fatalf("short payment gaps %.2f with excitation, %.2f without", bursty, calm)
	}
}
