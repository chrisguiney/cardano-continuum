// Command continuum runs and replays simulations.
//
//	continuum run --fake --seed N --out DIR [flags]
//	continuum replay --seed N [flags] DIR/events.log
//	continuum export EVENTS.LOG --out DIR
//	continuum image set <impl>=<ref> --network N [--pools N% | --pool ID] [--tier T] [--dry-run]
//
// replay takes the same population and chain flags as run; the log header's
// fingerprints refuse a log recorded under different ones.
package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"github.com/chrisguiney/cardano-continuum/internal/build"
	"github.com/chrisguiney/cardano-continuum/internal/engine"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/fakechain"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
	"github.com/chrisguiney/cardano-continuum/profiles"
)

// version goes into the log header; a log replays only under the binary
// version that wrote it.
const version = "m1"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	err := run(ctx, os.Args[1:], os.Stdout)
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, "continuum:", err)
		os.Exit(1)
	}
}

var errUsage = errors.New("usage: continuum run|replay|export|image [flags]")

func run(ctx context.Context, args []string, out io.Writer) error {
	if len(args) == 0 {
		return errUsage
	}
	switch args[0] {
	case "run":
		return cmdRun(ctx, args[1:], out)
	case "replay":
		return cmdReplay(ctx, args[1:], out)
	case "export":
		return cmdExport(ctx, args[1:], out)
	case "image":
		return cmdImage(ctx, args[1:], out, openKube)
	}
	return fmt.Errorf("%w: unknown command %q", errUsage, args[0])
}

// simFlags are the flags that fix a run's configuration; run and replay
// share them.
type simFlags struct {
	seed       uint64
	wallets    int
	profiles   string
	slotMs     int64
	epochSlots uint64
	k          uint64
	blockEvery uint64
	ttl        uint64
}

func (s *simFlags) register(fs *flag.FlagSet) {
	fs.Uint64Var(&s.seed, "seed", 1, "root seed of the run")
	fs.IntVar(&s.wallets, "wallets", 100, "wallets at genesis, spread evenly over the profiles")
	fs.StringVar(&s.profiles, "profiles", "", "profile file (default: the shipped M1 population)")
	fs.Int64Var(&s.slotMs, "slot-ms", 1000, "slot length in milliseconds")
	fs.Uint64Var(&s.epochSlots, "epoch-slots", 600, "slots per epoch")
	fs.Uint64Var(&s.k, "k", 5, "security parameter: blocks until a block is durable")
	fs.Uint64Var(&s.blockEvery, "block-every", 10, "slots between fake-chain blocks")
	fs.Uint64Var(&s.ttl, "ttl", 120, "slots a payment stays valid")
}

func (s *simFlags) config() (engine.Config, error) {
	raw := profiles.M1
	name := "m1.yaml"
	if s.profiles != "" {
		var err error
		if raw, err = os.ReadFile(s.profiles); err != nil {
			return engine.Config{}, err
		}
		name = s.profiles
	}
	ps, err := profile.ParseNamed(name, bytes.NewReader(raw))
	if err != nil {
		return engine.Config{}, err
	}
	reg, err := profile.NewRegistry(ps)
	if err != nil {
		return engine.Config{}, err
	}
	slots, err := sched.NewSlots(sched.Time(s.slotMs), s.epochSlots)
	if err != nil {
		return engine.Config{}, err
	}
	if s.wallets < 0 {
		return engine.Config{}, fmt.Errorf("--wallets %d", s.wallets)
	}
	names := reg.Names()
	var cohorts []engine.Cohort
	for i, n := range names {
		c := s.wallets / len(names)
		if i < s.wallets%len(names) {
			c++
		}
		cohorts = append(cohorts, engine.Cohort{Profile: n, Count: c})
	}
	pp := fakechain.DefaultParams()
	return engine.Config{
		Seed: engine.SeedFromInt(s.seed), Profiles: reg, Cohorts: cohorts, Slots: slots,
		K: s.k, BlockEvery: s.blockEvery, TTL: s.ttl, BinaryVersion: version,
		Build: build.Params{
			MinFeeA: uint64(pp.MinFeeA), MinFeeB: uint64(pp.MinFeeB),
			CoinsPerUtxoByte: uint64(pp.AdaPerUtxoByte), MaxTxSize: uint32(pp.MaxTxSize),
		},
	}, nil
}

func cmdRun(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	var sf simFlags
	sf.register(fs)
	fake := fs.Bool("fake", false, "run against the in-process fake chain (required in M1)")
	epochs := fs.Uint64("epochs", 2, "epochs to simulate")
	dir := fs.String("out", "", "directory for events.log (must not hold one)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if !*fake {
		return errors.New("run: only --fake is supported")
	}
	if *dir == "" {
		return errors.New("run: --out is required")
	}
	cfg, err := sf.config()
	if err != nil {
		return err
	}
	path := filepath.Join(*dir, "events.log")
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("run: %s exists; a fake run cannot resume, since its chain lived in the process", path)
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
	start := time.Now()
	r, err := engine.Start(ctx, cfg, engine.Deps{Clock: sched.NewVirtual(0), Log: mem, Node: node, Pacer: node})
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
	e := r.Engine()
	recs := mem.LastSeq()
	w := &writer{out: out}
	conserved := e.Supply() == genesis && chain.UnspentLovelace()+chain.Fees() == genesis
	w.printf("epochs %d, actors %d (genesis %d), blocks %d, records %d\n",
		epochs, e.Actors(), len(funds), len(chain.Blocks()), recs)
	w.printf("payments: submitted %d, confirmed %d, released %d, skipped %d, pending %d\n",
		kinds[engine.KindSubmitted], kinds[engine.KindConfirmed], kinds[engine.KindReleased], kinds[engine.KindSkipped], e.Pending())
	w.printf("value conserved: %t (genesis %d lovelace, fees %d)\n", conserved, genesis, chain.Fees())
	w.printf("elapsed %s, %.0f events/s\n", elapsed.Round(time.Millisecond), float64(recs)/elapsed.Seconds())
	if w.err != nil {
		return w.err
	}
	if !conserved {
		return errors.New("value not conserved")
	}
	return nil
}

// writer keeps the first write error so a run of prints checks it once.
type writer struct {
	out io.Writer
	err error
}

func (w *writer) printf(format string, a ...any) {
	if w.err == nil {
		_, w.err = fmt.Fprintf(w.out, format, a...)
	}
}

func cmdReplay(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("replay", flag.ContinueOnError)
	var sf simFlags
	sf.register(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("replay: want one log path")
	}
	cfg, err := sf.config()
	if err != nil {
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
	w := &writer{out: out}
	start := time.Now()
	diff, err := (&eventlog.Replayer{New: cfg.Factory(), Log: l, Header: &hdr}).Run(ctx)
	if err != nil {
		return err
	}
	w.printf("replayed %d records in %s: %d mismatches\n", l.LastSeq(), time.Since(start).Round(time.Millisecond), len(diff))
	for i, m := range diff {
		if i == 10 {
			w.printf("... %d more\n", len(diff)-10)
			break
		}
		w.printf("  %s\n", m)
	}
	if len(diff) > 0 {
		return errors.New("replay diverged from the log")
	}
	return w.err
}
