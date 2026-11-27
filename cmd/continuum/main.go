// Command continuum runs and replays simulations.
//
//	continuum run --fake --seed N --out DIR [flags]
//	continuum run --node HOST:PORT --state DIR --genesis FILE [flags]
//	continuum run --fake --attack dust-flood,front-run ... (one adversary per attack)
//	continuum compare CONTROL/events.log ATTACK/events.log
//	continuum replay --seed N [flags] DIR/events.log
//	continuum replay --image impl=ref [--live --network N --node HOST:PORT --genesis FILE] DIR/events.log
//	continuum bisect --dep D <good>..<bad> --repo DIR --check KIND|COMMAND [--dingo DIR] [--seed N]
//	continuum export EVENTS.LOG --out DIR
//	continuum serve --log EVENTS.LOG [--store DB] [--control LOG] [--addr 127.0.0.1:8088] [--controls]
//	continuum image set <impl>=<ref> --network N [--pools N% | --pool ID] [--tier T] [--dry-run]
//	continuum profiles index [--out FILE]
//	continuum build --dingo DIR [--with MODULE=PATH|SHA ...] [flags]
//	continuum harvest --repo URL|PATH --ref SHA --out DIR [--family F ...]
//	continuum vectors --repo URL|PATH --ref SHA --out DIR [--era E ...]
//	continuum scenario run ID|FILE|DIR ... [--lib DIR] [--target fake] [--seed N]
//	continuum scenario harvest --repo URL|PATH --ref SHA --out DIR [--walks N]
//	continuum scenario vectors FILE.vectors.jsonl ... [--sample N]
//
// replay takes the same population and chain flags as run; the log header's
// fingerprints refuse a log recorded under different ones.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/chrisguiney/cardano-continuum/internal/cli/attack"
	clibisect "github.com/chrisguiney/cardano-continuum/internal/cli/bisect"
	clibuild "github.com/chrisguiney/cardano-continuum/internal/cli/build"
	"github.com/chrisguiney/cardano-continuum/internal/cli/export"
	"github.com/chrisguiney/cardano-continuum/internal/cli/harvest"
	"github.com/chrisguiney/cardano-continuum/internal/cli/image"
	"github.com/chrisguiney/cardano-continuum/internal/cli/live"
	"github.com/chrisguiney/cardano-continuum/internal/cli/profiles"
	cliscenario "github.com/chrisguiney/cardano-continuum/internal/cli/scenario"
	"github.com/chrisguiney/cardano-continuum/internal/cli/serve"
	"github.com/chrisguiney/cardano-continuum/internal/cli/shared/lines"
	"github.com/chrisguiney/cardano-continuum/internal/cli/shared/simflags"
	"github.com/chrisguiney/cardano-continuum/internal/engine"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/fakechain"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
	"github.com/chrisguiney/cardano-continuum/internal/store"
)

func main() {
	// Kubernetes stops a pod with SIGTERM; the run then flushes and
	// checkpoints before exiting.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, os.Args[1:], os.Stdout)
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, "continuum:", err)
		os.Exit(1)
	}
}

var errUsage = errors.New("usage: continuum run|replay|compare|export|image|build|bisect|serve|harvest|vectors|scenario|profiles [flags]")

func run(ctx context.Context, args []string, out io.Writer) error {
	if len(args) == 0 {
		return errUsage
	}
	switch args[0] {
	case "run":
		return cmdRun(ctx, args[1:], out)
	case "replay":
		return cmdReplay(ctx, args[1:], out)
	case "compare":
		return attack.Run(ctx, args[1:], out)
	case "export":
		return export.Run(ctx, args[1:], out)
	case "image":
		return image.Run(ctx, args[1:], out)
	case "profiles":
		return profiles.Run(ctx, args[1:], out)
	case "build":
		return clibuild.Run(ctx, args[1:], out)
	case "bisect":
		return clibisect.Run(ctx, args[1:], out)
	case "serve":
		return serve.Run(ctx, args[1:], out)
	case "harvest":
		return harvest.Run(ctx, args[1:], out)
	case "vectors":
		return harvest.Vectors(ctx, args[1:], out)
	case "scenario":
		return cliscenario.Run(ctx, args[1:], out)
	}
	return fmt.Errorf("%w: unknown command %q", errUsage, args[0])
}

func cmdRun(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	var sf simflags.Flags
	sf.Register(fs)
	fake := fs.Bool("fake", false, "run against the in-process fake chain")
	epochs := fs.Uint64("epochs", 2, "epochs to simulate (a live run: 0 runs until stopped)")
	dir := fs.String("out", "", "directory for events.log (must not hold one)")
	var lf live.Flags
	lf.Register(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if !*fake {
		if lf.Node == "" {
			return errors.New("run: --fake or --node is required")
		}
		return live.Run(ctx, sf, lf, *epochs)
	}
	if *dir == "" {
		return errors.New("run: --out is required")
	}
	cfg, err := sf.Config()
	if err != nil {
		return err
	}
	if err := simflags.ClearFake(&cfg); err != nil {
		return err
	}
	path := filepath.Join(*dir, "events.log")
	dbPath := filepath.Join(*dir, "store.db")
	for _, p := range []string{path, dbPath} {
		if _, err := os.Stat(p); err == nil {
			return fmt.Errorf("run: %s exists; a fake run cannot resume, since its chain lived in the process", p)
		}
	}
	chain, err := engine.NewFakeChain(&cfg)
	if err != nil {
		return err
	}
	node := &engine.FakeNode{Chain: chain, Slots: cfg.Slots, BlockEvery: cfg.BlockEvery}
	// The fake chain dies with the process, so a fake run can never resume
	// and an fsync per input would buy nothing: the log is kept in memory
	// and written out once at the end.
	mem := &eventlog.MemLog{}
	// The store is the only record of genesis actors' profiles: their
	// Spawned records are configuration, not decisions, so serve reads them
	// from here. Its checkpoint at the end of the run covers the whole log.
	if err := os.MkdirAll(*dir, 0o750); err != nil {
		return err
	}
	db, err := store.Open(ctx, dbPath, store.WithLog(mem))
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	start := time.Now()
	r, err := engine.Start(ctx, cfg, engine.Deps{Clock: sched.NewVirtual(0), Log: mem, Node: node, Pacer: node, Store: db})
	if err != nil {
		return err
	}
	runErr := r.Run(ctx, cfg.Slots.EpochStart(*epochs))
	elapsed := time.Since(start)
	if err := writeLog(ctx, path, mem); err != nil {
		return err
	}
	if runErr != nil {
		return runErr
	}
	return summarize(out, cfg, r, chain, mem, elapsed, *epochs)
}

func writeLog(ctx context.Context, path string, mem *eventlog.MemLog) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	fl, err := eventlog.Open(path)
	if err != nil {
		return err
	}
	var batch []eventlog.Record
	err = mem.Scan(ctx, 1, func(rec eventlog.Record) error {
		if batch = append(batch, rec); len(batch) == 4096 {
			err := fl.Append(ctx, batch...)
			batch = batch[:0]
			return err
		}
		return nil
	})
	if err == nil {
		err = fl.Append(ctx, batch...)
	}
	return errors.Join(err, fl.Close())
}

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

func cmdReplay(ctx context.Context, args []string, out io.Writer) error {
	return cmdReplayWith(ctx, args, out, realReplayDeps())
}

func cmdReplayWith(ctx context.Context, args []string, out io.Writer, deps replayDeps) error {
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
		return replayImage(ctx, *image, fs.Arg(0), *live, sf, lf, out, deps)
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
