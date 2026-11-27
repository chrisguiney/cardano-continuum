package pace

import (
	"math"

	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

// The market signal is a Hawkes process over the engine's own submissions,
// with an exponential kernel: intensity = mu + excess, where each
// submission adds alpha to excess and excess decays at rate beta. Only the
// excess and when it was last updated are state, so the snapshot carries
// two numbers instead of the event history sched.Hawkes keeps.
const (
	marketMu    = 0.1 // submissions per second with no excitation
	marketAlpha = 0.5
	marketBeta  = 1.0
)

// marketBaseline is the process's long-run mean rate, mu / (1 - alpha/beta):
// Burst is 1 when submissions run at it.
const marketBaseline = marketMu / (1 - marketAlpha/marketBeta)

// Market is the market signal's state, as the snapshot holds it.
type Market struct {
	Excess float64    `json:"excess,omitzero"`
	At     sched.Time `json:"at,omitzero"`
}

// decayed is the excess at now; times before the last update read as it.
func (m Market) decayed(now sched.Time) float64 {
	dt := max(0, float64(now-m.At)/1000)
	return m.Excess * math.Exp(-marketBeta*dt)
}

// excite records a submission at at.
func (m *Market) excite(at sched.Time) {
	m.Excess = m.decayed(at) + marketAlpha
	m.At = max(m.At, at)
}

// burst is the intensity at now over the long-run baseline.
func (m Market) burst(now sched.Time) float64 {
	return (marketMu + m.decayed(now)) / marketBaseline
}
