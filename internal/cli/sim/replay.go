package sim

import (
	"context"
	"errors"
	"flag"
	"io"
	"time"

	"github.com/chrisguiney/cardano-continuum/internal/cli/internal/lines"
	"github.com/chrisguiney/cardano-continuum/internal/cli/internal/simflags"
	"github.com/chrisguiney/cardano-continuum/internal/cli/live"
	"github.com/chrisguiney/cardano-continuum/internal/cli/rerun"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
)

// Replay handles `continuum replay`: it replays a log under the flags that
// recorded it and reports where the engine diverges, or with --image re-runs a
// live log on another node build.
func Replay(ctx context.Context, args []string, out io.Writer) error {
	return ReplayWith(ctx, args, out, rerun.Real())
}

// ReplayWith is Replay over the outside world a live --image touches.
func ReplayWith(ctx context.Context, args []string, out io.Writer, deps rerun.Deps) error {
	fs := flag.NewFlagSet("replay", flag.ContinueOnError)
	var sf simflags.Flags
	sf.Register(fs)
	var lf live.Flags
	lf.Register(fs)
	image := fs.String("image", "", "replay against a node build: <impl>=<ref>; needs --live to re-run a live log")
	live := fs.Bool("live", false, "the log was recorded by a live run (with --image: re-run it on --network)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("replay: want one log path")
	}
	if *image != "" {
		return rerun.Image(ctx, *image, fs.Arg(0), *live, sf, lf, out, deps)
	}
	cfg, err := sf.Config()
	if err != nil {
		return err
	}
	if err := simflags.ClearFake(&cfg); err != nil {
		return err
	}
	hdr, err := cfg.Header()
	if err != nil {
		return err
	}
	l, err := eventlog.Open(fs.Arg(0))
	if err != nil {
		return err
	}
	defer func() { _ = l.Close() }()
	w := &lines.Writer{Out: out}
	start := time.Now()
	diff, err := (&eventlog.Replayer{New: cfg.Factory(), Log: l, Header: &hdr}).Run(ctx)
	if err != nil {
		return err
	}
	w.Printf("replayed %d records in %s: %d mismatches\n", l.LastSeq(), time.Since(start).Round(time.Millisecond), len(diff))
	for i, m := range diff {
		if i == 10 {
			w.Printf("... %d more\n", len(diff)-10)
			break
		}
		w.Printf("  %s\n", m)
	}
	if len(diff) > 0 {
		return errors.New("replay diverged from the log")
	}
	return w.Err
}
