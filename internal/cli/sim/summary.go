package sim

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/chrisguiney/cardano-continuum/internal/cli/attack"
	"github.com/chrisguiney/cardano-continuum/internal/cli/shared/lines"
	"github.com/chrisguiney/cardano-continuum/internal/engine"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/fakechain"
)

// summarize prints a fake run's report and fails it when value was not
// conserved.
func summarize(out io.Writer, cfg engine.Config, r *engine.Runner, chain *fakechain.Chain, mem *eventlog.MemLog, elapsed time.Duration, epochs uint64) error {
	kinds := map[string]int{}
	if err := mem.Scan(context.Background(), 1, func(rec eventlog.Record) error {
		kinds[rec.Kind]++
		return nil
	}); err != nil {
		return err
	}
	funds, err := cfg.Genesis()
	if err != nil {
		return err
	}
	var genesis uint64
	for _, f := range funds {
		genesis += f.Lovelace
	}
	e := r.Engine().Summary()
	recs := mem.LastSeq()
	w := &lines.Writer{Out: out}
	// The chain also holds the genesis pools' owners' stake, which no
	// actor owns, and the deposits certificates and proposals hold.
	owners, _, err := engine.GenesisStaking(&cfg)
	if err != nil {
		return err
	}
	onChain := genesis
	for _, o := range owners {
		onChain += o.Lovelace
	}
	conserved := e.Supply == genesis && chain.UnspentLovelace()+chain.Fees()+chain.Deposits() == onChain
	w.Printf("epochs %d, actors %d (genesis %d), blocks %d, records %d\n",
		epochs, e.Actors, len(funds), len(chain.Blocks()), recs)
	w.Printf("payments: submitted %d, confirmed %d, released %d, skipped %d, pending %d\n",
		kinds[engine.KindSubmitted], kinds[engine.KindConfirmed], kinds[engine.KindReleased], kinds[engine.KindSkipped], e.Pending)
	w.Printf("value conserved: %t (genesis %d lovelace, fees %d)\n", conserved, genesis, chain.Fees())
	t, err := engine.AuditLog(context.Background(), mem)
	if err != nil {
		return err
	}
	w.Printf("mistakes %t: rejections %s; findings %d (fatal %d); lost %d lovelace\n",
		cfg.Mistakes, classCounts(t.Rejections), t.Findings, t.Fatal, t.Lost)
	gov, err := countGov(context.Background(), mem)
	if err != nil {
		return err
	}
	w.Printf("%s", gov.line())
	if err := attack.Summarize(context.Background(), w, mem); err != nil {
		return err
	}
	w.Printf("elapsed %s, %.0f events/s\n", elapsed.Round(time.Millisecond), float64(recs)/elapsed.Seconds())
	if w.Err != nil {
		return w.Err
	}
	if !conserved {
		return errors.New("value not conserved")
	}
	return nil
}

// classCounts prints counts by class, sorted by class.
func classCounts(m map[string]int) string {
	if len(m) == 0 {
		return "none"
	}
	parts := make([]string, 0, len(m))
	for _, c := range slices.Sorted(maps.Keys(m)) {
		parts = append(parts, fmt.Sprintf("%s=%d", c, m[c]))
	}
	return strings.Join(parts, " ")
}
