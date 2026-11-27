package harvest

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"sort"

	"github.com/chrisguiney/cardano-continuum/internal/cli/internal/flagx"
	"github.com/chrisguiney/cardano-continuum/internal/cli/internal/tlaflags"
	"github.com/chrisguiney/cardano-continuum/internal/tla"
)

// Run runs TLC on every *Bug.cfg of a model repository and writes the
// violating traces as seeds.
func Run(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("harvest", flag.ContinueOnError)
	var f tlaflags.Flags
	var fams flagx.ListFlag
	f.Register(fs)
	fs.Var(&fams, "family", "limit to this specs/<family>; repeatable")
	if err := fs.Parse(args); err != nil {
		return err
	}
	tools, cleanup, err := f.Resolve("harvest")
	if err != nil {
		return err
	}
	defer cleanup()
	sum, err := tla.Harvest(ctx, tla.Options{
		Repo: f.Repo, Ref: f.Ref, CacheDir: f.Cache, Out: f.Out, Families: fams,
		Tools: tools, Timeout: f.Timeout, Workers: f.Workers,
	})
	if err != nil {
		return fmt.Errorf("harvest: %w", err)
	}
	var bad []string
	for _, s := range sum.Seeds {
		if s.Status != tla.Violation {
			bad = append(bad, fmt.Sprintf("  %s: %s", s.Status, s.Name))
		}
	}
	sort.Strings(bad)
	_, _ = fmt.Fprintf(out, "commit %s: %d bug cfgs: %d violations, %d broken, %d timed out, %d passed\n",
		sum.Commit, sum.Total, sum.Violations, sum.Broken, sum.TimedOut, sum.Passed)
	for _, b := range bad {
		_, _ = fmt.Fprintln(out, b)
	}
	return nil
}

// Vectors exports the Utxo.tla boundary-transaction table of a model
// repository.
func Vectors(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("vectors", flag.ContinueOnError)
	var f tlaflags.Flags
	var eras flagx.ListFlag
	f.Register(fs)
	fs.Var(&eras, "era", "limit to this era (Shelley, Allegra, Mary, Alonzo, Babbage); repeatable")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if f.Ref == "" && f.Repo == "" {
		return errors.New("vectors: --repo is required")
	}
	tools, cleanup, err := f.Resolve("vectors")
	if err != nil {
		return err
	}
	defer cleanup()
	counts, err := tla.ExportVectors(ctx, tla.VectorOptions{
		Repo: f.Repo, Ref: f.Ref, CacheDir: f.Cache, Out: f.Out, Eras: eras,
		Tools: tools, Timeout: f.Timeout,
	})
	if err != nil {
		return fmt.Errorf("vectors: %w", err)
	}
	for _, era := range tla.UtxoEras {
		if n, ok := counts[era]; ok {
			_, _ = fmt.Fprintf(out, "%s: %d vectors\n", era, n)
		}
	}
	return nil
}
