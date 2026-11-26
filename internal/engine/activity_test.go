package engine

import (
	"math"
	"strings"
	"testing"

	"github.com/chrisguiney/cardano-continuum/internal/profile"
)

// wakeStats is the wake-ups per actor per second and the coefficient of
// variation of the gaps between an actor's wake-ups, over a run.
func wakeStats(t *testing.T, yaml string) (rate, cv float64) {
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
	const until = 1_200_000
	w.run(until, 50, nil)
	last := map[uint64]int64{}
	var n, sum, sq float64
	for _, rec := range logRecords(t, w.log) {
		if rec.Kind != KindIntentChosen {
			continue
		}
		if prev, ok := last[uint64(rec.Actor)]; ok {
			g := float64(int64(rec.At) - prev)
			n, sum, sq = n+1, sum+g, sq+g*g
		}
		last[uint64(rec.Actor)] = int64(rec.At)
	}
	m := sum / n
	return 1000 / m, math.Sqrt(sq/n-m*m) / m
}

// A profile's Hawkes parameters make its actors' wake-ups bursty at the
// same mean rate: the base rate is cut to mu = rate * (1 - alpha/beta), so
// the excited process averages what the profile asks for.
func TestHawkesActivityIsBursty(t *testing.T) {
	calm := strings.Replace(testProfileYAML, "      diurnal: 0.3\n", "", 1)
	excited := strings.Replace(calm, "        delegate: 0.1\n", "        delegate: 0.1\n      hawkes: {alpha: 0.3, beta: 0.4}\n", 1)
	r0, c0 := wakeStats(t, calm)
	r1, c1 := wakeStats(t, excited)
	t.Logf("calm rate %.3f/s cv %.2f; excited rate %.3f/s cv %.2f", r0, c0, r1, c1)
	if c1 < c0+0.3 {
		t.Errorf("excited gaps cv %.2f, calm %.2f: not bursty", c1, c0)
	}
	if math.Abs(math.Log(r1/r0)) > 0.35 {
		t.Errorf("excited rate %.3f against calm %.3f: the mean moved", r1, r0)
	}
}
