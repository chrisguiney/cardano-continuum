package adversary

import (
	"fmt"
	"math"
	"slices"
	"strings"
)

// Metrics is what one run did to its honest users, its node and its
// adversaries, folded from the run's log (engine.Measure). The impact of
// an attack is the difference between a run with adversaries and a control
// run with the same seed and none.
type Metrics struct {
	// HonestIntents counts the honest intents that reached a node;
	// HonestEnded those that confirmed or were released, HonestFailed the
	// released ones.
	HonestIntents, HonestEnded, HonestFailed int
	// Latency is each confirmed honest intent's time from its first
	// submission to its confirmation, in seconds.
	Latency []float64
	// Retries counts honest attempts past each intent's first.
	Retries int
	// Grabbed is the lovelace of honest locked outputs adversaries spent.
	Grabbed uint64
	// UtxoGrowth is the outputs blocks created less those they spent.
	UtxoGrowth int64
	// Rejections counts every rejection a node answered, any sender.
	Rejections int
	// Findings and Fatal count the oracle's findings.
	Findings, Fatal int
	// AttackSteps counts the attacks made, AttackCost the lovelace they
	// were charged, AttackLanded those confirmed and AttackDetected those
	// a node rejected or the oracle flagged.
	AttackSteps    int
	AttackCost     uint64
	AttackLanded   int
	AttackDetected int
}

// FailureRate is the share of ended honest intents that were released.
func (m Metrics) FailureRate() float64 { return ratio(m.HonestFailed, m.HonestEnded) }

// Impact is one metric in a control run and a treated one.
type Impact struct {
	Metric           string
	Control, Treated float64
}

// Delta is Treated less Control.
func (i Impact) Delta() float64 { return i.Treated - i.Control }

type metric struct {
	name  string
	value func(Metrics) float64
}

var metrics = []metric{
	{"honest_intents", func(m Metrics) float64 { return float64(m.HonestIntents) }},
	{"honest_failure_rate", Metrics.FailureRate},
	{"honest_latency_p50_s", func(m Metrics) float64 { return percentile(m.Latency, 0.5) }},
	{"honest_latency_p99_s", func(m Metrics) float64 { return percentile(m.Latency, 0.99) }},
	{"honest_retries", func(m Metrics) float64 { return float64(m.Retries) }},
	{"honest_value_grabbed_ada", func(m Metrics) float64 { return float64(m.Grabbed) / 1e6 }},
	{"utxo_growth", func(m Metrics) float64 { return float64(m.UtxoGrowth) }},
	{"node_rejections", func(m Metrics) float64 { return float64(m.Rejections) }},
	{"findings", func(m Metrics) float64 { return float64(m.Findings) }},
	{"fatal_findings", func(m Metrics) float64 { return float64(m.Fatal) }},
	{"attack_steps", func(m Metrics) float64 { return float64(m.AttackSteps) }},
	{"attack_cost_ada", func(m Metrics) float64 { return float64(m.AttackCost) / 1e6 }},
	{"attack_landed", func(m Metrics) float64 { return float64(m.AttackLanded) }},
	{"attack_detected", func(m Metrics) float64 { return float64(m.AttackDetected) }},
}

// MetricNames lists the metrics in table order. A profile's
// success_metric names one of them.
func MetricNames() []string {
	out := make([]string, len(metrics))
	for i, m := range metrics {
		out[i] = m.name
	}
	return out
}

// Compare returns one impact row per metric, in MetricNames order.
func Compare(control, treated Metrics) []Impact {
	out := make([]Impact, len(metrics))
	for i, m := range metrics {
		out[i] = Impact{Metric: m.name, Control: m.value(control), Treated: m.value(treated)}
	}
	return out
}

// Table renders rows as a fixed-width table; the rows named in mark (the
// adversaries' success metrics) carry a star.
func Table(rows []Impact, mark ...string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%-22s %12s %12s %12s\n", "metric", "control", "attack", "delta")
	for _, r := range rows {
		name := r.Metric
		if slices.Contains(mark, name) {
			name += " *"
		}
		fmt.Fprintf(&b, "%-22s %12s %12s %12s\n", name, num(r.Control), num(r.Treated), num(r.Delta()))
	}
	return b.String()
}

func num(x float64) string {
	if x == math.Trunc(x) && math.Abs(x) < 1e12 {
		return fmt.Sprintf("%.0f", x)
	}
	return fmt.Sprintf("%.3f", x)
}

func ratio(a, b int) float64 {
	if b == 0 {
		return 0
	}
	return float64(a) / float64(b)
}

// percentile is the nearest-rank q-quantile of xs; zero for none.
func percentile(xs []float64, q float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := slices.Clone(xs)
	slices.Sort(s)
	i := int(math.Ceil(q*float64(len(s)))) - 1
	return s[min(max(i, 0), len(s)-1)]
}
