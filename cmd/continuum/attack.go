package main

import (
	"context"

	"errors"
	"fmt"
	"io"
	"slices"

	"github.com/chrisguiney/cardano-continuum/internal/adversary"
	"github.com/chrisguiney/cardano-continuum/internal/cli/shared/lines"
	"github.com/chrisguiney/cardano-continuum/internal/cli/shared/simflags"
	"github.com/chrisguiney/cardano-continuum/internal/engine"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
)

// summarizeAttacks prints what a run's adversaries did, when it had any.
func summarizeAttacks(ctx context.Context, w *lines.Writer, l eventlog.Reader) error {
	m, err := engine.Measure(ctx, l)
	if err != nil {
		return err
	}
	if m.AttackSteps == 0 {
		return nil
	}
	w.Printf("attacks: steps %d, cost %.3f ADA, landed %d, detected %d; honest failure rate %.3f, utxo growth %d\n",
		m.AttackSteps, float64(m.AttackCost)/1e6, m.AttackLanded, m.AttackDetected,
		m.FailureRate(), m.UtxoGrowth)
	return nil
}

// cmdCompare prints the impact table of an attack run's log against a
// control run's: same seed and flags, adversaries off and on.
func cmdCompare(ctx context.Context, args []string, out io.Writer) error {
	if len(args) != 2 {
		return errors.New("usage: continuum compare <control-log> <attack-log>")
	}
	var ms [2]adversary.Metrics
	var names []adversary.Name
	for i, path := range args {
		l, err := eventlog.Open(path)
		if err != nil {
			return err
		}
		ms[i], err = engine.Measure(ctx, l)
		if i == 1 && err == nil {
			names, err = attacksIn(ctx, l)
		}
		if err = errors.Join(err, l.Close()); err != nil {
			return fmt.Errorf("compare %s: %w", path, err)
		}
	}
	if ms[0].AttackSteps > 0 {
		return fmt.Errorf("compare: %s is not a control run: it has %d attacks", args[0], ms[0].AttackSteps)
	}
	var mark []string
	for _, n := range names {
		mark = append(mark, adversary.SuccessMetric(n))
	}
	w := &lines.Writer{Out: out}
	w.Printf("%s", adversary.Table(adversary.Compare(ms[0], ms[1]), mark...))
	w.Printf("* an attack's success metric (%s)\n", simflags.JoinNames(names))
	return w.Err
}

// attacksIn lists the attacks a log's Attack decisions name, sorted.
func attacksIn(ctx context.Context, l eventlog.Reader) ([]adversary.Name, error) {
	var out []adversary.Name
	err := l.Scan(ctx, 1, func(rec eventlog.Record) error {
		if rec.Kind != engine.KindAttack {
			return nil
		}
		var a engine.Attack
		if err := eventlog.Decode(rec.Body, &a); err != nil {
			return err
		}
		if n := adversary.Name(a.Name); !slices.Contains(out, n) {
			out = append(out, n)
		}
		return nil
	})
	slices.Sort(out)
	return out, err
}
