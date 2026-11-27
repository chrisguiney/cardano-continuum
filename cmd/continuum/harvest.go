package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/chrisguiney/cardano-continuum/internal/cli/shared/flagx"
	"github.com/chrisguiney/cardano-continuum/internal/tla"
)

// tlaFlags are the flags harvest and vectors share.
type tlaFlags struct {
	repo, ref, out, cache, tlc, jar string
	timeout                         time.Duration
	workers                         int
}

func (f *tlaFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&f.repo, "repo", "", "model repository URL or path")
	fs.StringVar(&f.ref, "ref", "", "commit to pin; without it --repo must be a directory holding specs/")
	fs.StringVar(&f.out, "out", "", "output directory")
	fs.StringVar(&f.cache, "cache", "", "clone cache (default: the user cache directory)")
	fs.StringVar(&f.tlc, "tlc", "models/bin/tlc", "TLC wrapper script")
	fs.StringVar(&f.jar, "jar", envOr("TLA2TOOLS_JAR", "models/.tools/tla2tools-1.8.0.jar"), "tla2tools jar whose TLC supports -dumpTrace")
	fs.DurationVar(&f.timeout, "timeout", 4*time.Minute, "bound for each TLC run")
	fs.IntVar(&f.workers, "workers", 2, "TLC runs in flight")
}

func envOr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

// resolve checks the required flags, fills the cache default and creates the
// private temp directory TLC runs use; the caller removes it.
func (f *tlaFlags) resolve(cmd string) (tla.Tools, func(), error) {
	if f.repo == "" || f.out == "" {
		return tla.Tools{}, nil, fmt.Errorf("%s: --repo and --out are required", cmd)
	}
	if f.cache == "" {
		base, err := os.UserCacheDir()
		if err != nil {
			return tla.Tools{}, nil, fmt.Errorf("%s: no cache directory, pass --cache: %w", cmd, err)
		}
		f.cache = filepath.Join(base, "continuum", "tla")
	}
	tmp, err := os.MkdirTemp("", "continuum-tla-")
	if err != nil {
		return tla.Tools{}, nil, fmt.Errorf("%s: %w", cmd, err)
	}
	// TLC runs in the model's directory, so relative tool paths must be made
	// absolute first.
	tlc, err := filepath.Abs(f.tlc)
	if err != nil {
		return tla.Tools{}, nil, fmt.Errorf("%s: %w", cmd, err)
	}
	jar, err := filepath.Abs(f.jar)
	if err != nil {
		return tla.Tools{}, nil, fmt.Errorf("%s: %w", cmd, err)
	}
	tools := tla.Tools{TLC: tlc, Jar: jar, Tmp: tmp}
	return tools, func() { _ = os.RemoveAll(tmp) }, nil
}

func cmdHarvest(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("harvest", flag.ContinueOnError)
	var f tlaFlags
	var fams flagx.ListFlag
	f.register(fs)
	fs.Var(&fams, "family", "limit to this specs/<family>; repeatable")
	if err := fs.Parse(args); err != nil {
		return err
	}
	tools, cleanup, err := f.resolve("harvest")
	if err != nil {
		return err
	}
	defer cleanup()
	sum, err := tla.Harvest(ctx, tla.Options{
		Repo: f.repo, Ref: f.ref, CacheDir: f.cache, Out: f.out, Families: fams,
		Tools: tools, Timeout: f.timeout, Workers: f.workers,
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

func cmdVectors(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("vectors", flag.ContinueOnError)
	var f tlaFlags
	var eras flagx.ListFlag
	f.register(fs)
	fs.Var(&eras, "era", "limit to this era (Shelley, Allegra, Mary, Alonzo, Babbage); repeatable")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if f.ref == "" && f.repo == "" {
		return errors.New("vectors: --repo is required")
	}
	tools, cleanup, err := f.resolve("vectors")
	if err != nil {
		return err
	}
	defer cleanup()
	counts, err := tla.ExportVectors(ctx, tla.VectorOptions{
		Repo: f.repo, Ref: f.ref, CacheDir: f.cache, Out: f.out, Eras: eras,
		Tools: tools, Timeout: f.timeout,
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
