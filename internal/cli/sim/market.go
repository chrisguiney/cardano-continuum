package sim

import (
	"context"
	"fmt"

	"github.com/chrisguiney/cardano-continuum/internal/engine"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

// marketLine is the summary's markets line: settled trades by exchange and
// side, births by currency, and the largest ADA/USD drawdown with the split
// of the wallets under water in it. Empty for a run without a market.
func marketLine(ctx context.Context, cfg engine.Config, r eventlog.Reader) (string, error) {
	if cfg.Markets == nil {
		return "", nil
	}
	var tp engine.Tape
	if err := r.Scan(ctx, 1, func(rec eventlog.Record) error {
		if rec.Class != eventlog.Decision {
			return nil
		}
		return tp.Add(rec)
	}); err != nil {
		return "", err
	}
	s := tp.Summary()
	sides := map[string]map[string]int{"buy": {}, "sell": {}}
	for _, f := range s.Flows {
		sides[f.Side][f.Exchange] += f.Trades
	}
	d := s.Drawdown
	return fmt.Sprintf("markets: buys %s; sells %s; births %s; largest drawdown %.1f%% (%ds-%ds): hodlers %d, sellers %d\n",
		classCounts(sides["buy"]), classCounts(sides["sell"]), classCounts(s.Births),
		100*d.Depth, d.Peak/sched.Time(1000), d.Trough/sched.Time(1000), d.Hodlers, d.Sellers), nil
}
