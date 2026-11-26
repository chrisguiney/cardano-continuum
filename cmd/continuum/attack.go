package main

import (
	"context"

	"github.com/blinklabs-io/gouroboros/ledger/conway"

	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/chrisguiney/cardano-continuum/internal/adversary"
	"github.com/chrisguiney/cardano-continuum/internal/engine"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/fakechain"
	"github.com/chrisguiney/cardano-continuum/internal/genesis"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
)

// attackNames parses --attack: a comma-separated list of tier-1 attacks,
// one adversary each.
func (s *simFlags) attackNames() ([]adversary.Name, error) {
	if s.attacks == "" {
		return nil, nil
	}
	var out []adversary.Name
	for _, part := range strings.Split(s.attacks, ",") {
		n, err := adversary.Parse(strings.TrimSpace(part))
		if err != nil {
			return nil, fmt.Errorf("--attack: %w", err)
		}
		if !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	return out, nil
}

// chainParams are the fake chain's parameters: the devnet genesis with
// every override the --attack attacks need (adversary.GenesisOverrides),
// which a devnet allows only on purpose.
func (s *simFlags) chainParams() (conway.ConwayProtocolParameters, error) {
	ns, err := s.attackNames()
	if err != nil {
		return conway.ConwayProtocolParameters{}, err
	}
	spec := genesis.Default()
	for _, n := range ns {
		for _, o := range adversary.GenesisOverrides(n) {
			ov, err := genesis.ParseOverride(o)
			if err != nil {
				return conway.ConwayProtocolParameters{}, fmt.Errorf("--attack %s: %w", n, err)
			}
			ov.Apply(&spec)
			spec.Allow.InconsistentExUnits = true
		}
	}
	return fakechain.ParamsFor(spec)
}

// withAdversaries adds the built-in profile of every --attack to ps and
// one actor of each to cohorts, after every honest one, so the honest
// actors keep the IDs, keys and streams they have in a control run.
func (s *simFlags) withAdversaries(ps []profile.Profile, cohorts []engine.Cohort) ([]profile.Profile, []engine.Cohort, error) {
	ns, err := s.attackNames()
	if err != nil || len(ns) == 0 {
		return ps, cohorts, err
	}
	advs, err := adversary.Profiles(ns...)
	if err != nil {
		return nil, nil, err
	}
	for _, n := range ns {
		cohorts = append(cohorts, engine.Cohort{Profile: adversary.ProfileName(n), Count: 1})
	}
	return append(ps, advs...), cohorts, nil
}

// clearFake clears a fake run's adversaries: the in-process chain is a
// devnet with internal/genesis's magic, and chain.Guard checks it.
func clearFake(cfg *engine.Config) error {
	g := engine.FakeGenesis(cfg, genesis.Default().Magic)
	c, err := adversary.Clear(g, g)
	cfg.Adversaries = c
	return err
}

// summarizeAttacks prints what a run's adversaries did, when it had any.
func summarizeAttacks(ctx context.Context, w *writer, l eventlog.Reader) error {
	m, err := engine.Measure(ctx, l)
	if err != nil {
		return err
	}
	if m.AttackSteps == 0 {
		return nil
	}
	w.printf("attacks: steps %d, cost %.3f ADA, landed %d, detected %d; honest failure rate %.3f, utxo growth %d\n",
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
	w := &writer{out: out}
	w.printf("%s", adversary.Table(adversary.Compare(ms[0], ms[1]), mark...))
	w.printf("* an attack's success metric (%s)\n", joinNames(names))
	return w.err
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

func joinNames(ns []adversary.Name) string {
	s := make([]string, len(ns))
	for i, n := range ns {
		s[i] = string(n)
	}
	return strings.Join(s, ", ")
}
