package engine

import (
	"context"

	"github.com/chrisguiney/cardano-continuum/internal/adversary"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/meter"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/tape"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
)

// Log analytics live in internal/engine/internal/meter; they read records
// and never touch an Engine.
type (
	Tally      = meter.Tally
	Audit      = meter.Audit
	Meter      = meter.Meter
	AttackStep = meter.AttackStep
	CostPoint  = meter.CostPoint
	AttackStat = meter.AttackStat
	// Tape folds the market records (internal/engine/internal/tape).
	Tape          = tape.Tape
	MarketSummary = tape.Summary
	MarketFlow    = tape.Flow
	Drawdown      = tape.Drawdown
	MarketPoint   = tape.Point
)

// Measure folds a run's log into the metrics adversary.Compare reads; see
// meter.Measure.
func Measure(ctx context.Context, r eventlog.Reader) (adversary.Metrics, error) {
	return meter.Measure(ctx, r)
}

// AuditLog folds a log into a Tally; see meter.AuditLog.
func AuditLog(ctx context.Context, r eventlog.Reader) (Tally, error) {
	return meter.AuditLog(ctx, r)
}

// NewMeter returns an empty Meter; see meter.NewMeter.
func NewMeter() *Meter { return meter.NewMeter() }
