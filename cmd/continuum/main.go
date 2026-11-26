// Command continuum runs and replays simulations.
//
//	continuum run --fake --seed N --out DIR [flags]
//	continuum run --node HOST:PORT --state DIR --genesis FILE [flags]
//	continuum replay --seed N [flags] DIR/events.log
//	continuum replay --image impl=ref [--live --network N --node HOST:PORT --genesis FILE] DIR/events.log
//	continuum bisect --dep D <good>..<bad> --repo DIR --check KIND|COMMAND [--dingo DIR] [--seed N]
//	continuum export EVENTS.LOG --out DIR
//	continuum serve --log EVENTS.LOG [--store DB] [--addr 127.0.0.1:8088] [--controls]
//	continuum image set <impl>=<ref> --network N [--pools N% | --pool ID] [--tier T] [--dry-run]
//	continuum profiles index [--out FILE]
//	continuum build --dingo DIR [--with MODULE=PATH|SHA ...] [flags]
//	continuum harvest --repo URL|PATH --ref SHA --out DIR [--family F ...]
//	continuum vectors --repo URL|PATH --ref SHA --out DIR [--era E ...]
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
	"maps"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/chrisguiney/cardano-continuum/internal/build"
	"github.com/chrisguiney/cardano-continuum/internal/engine"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/fakechain"
	"github.com/chrisguiney/cardano-continuum/internal/genesis"
	"github.com/chrisguiney/cardano-continuum/internal/oracle"
	"github.com/chrisguiney/cardano-continuum/internal/policy"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
	"github.com/chrisguiney/cardano-continuum/internal/store"
)

// version goes into the log header; a log replays only under the binary
// version that wrote it.
const version = "m1"

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

var errUsage = errors.New("usage: continuum run|replay|export|image|build|bisect|serve|harvest|vectors|profiles [flags]")

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
	case "profiles":
		return cmdProfiles(args[1:], out)
	case "build":
		return cmdBuild(ctx, args[1:], out)
	case "bisect":
		return cmdBisect(ctx, args[1:], out)
	case "serve":
		return cmdServe(ctx, args[1:], out, openKube)
	case "harvest":
		return cmdHarvest(ctx, args[1:], out)
	case "vectors":
		return cmdVectors(ctx, args[1:], out)
	}
	return fmt.Errorf("%w: unknown command %q", errUsage, args[0])
}

// simFlags are the flags that fix a run's configuration; run and replay
// share them.
type simFlags struct {
	seed       uint64
	wallets    int
	profiles   string
	scale      float64
	slotMs     int64
	epochSlots uint64
	k          uint64
	blockEvery uint64
	ttl        uint64
	pools      int
	floor      uint64
	mistakes   string
}

func (s *simFlags) register(fs *flag.FlagSet) {
	fs.Uint64Var(&s.seed, "seed", 1, "root seed of the run")
	fs.IntVar(&s.wallets, "wallets", 100, "wallets at genesis, spread evenly over the profiles")
	fs.StringVar(&s.profiles, "profiles", "", "shipped profile set (m1, m3, preview, preprod) or a profile file (default: m1)")
	fs.Float64Var(&s.scale, "scale", 1, "multiply every profile's base rates by this factor")
	fs.Int64Var(&s.slotMs, "slot-ms", 1000, "slot length in milliseconds")
	fs.Uint64Var(&s.epochSlots, "epoch-slots", 600, "slots per epoch")
	fs.Uint64Var(&s.k, "k", 5, "security parameter: blocks until a block is durable")
	fs.Uint64Var(&s.blockEvery, "block-every", 10, "slots between fake-chain blocks")
	fs.Uint64Var(&s.ttl, "ttl", 120, "slots a payment stays valid")
	fs.Uint64Var(&s.floor, "floor", 50, "stake floor: the percentage of each schedule's stake forging pools must hold, one pool's loss included")
	fs.StringVar(&s.mistakes, "mistakes", "", "honest mistakes: on or off (default: on when a profile's competence can fall below 1)")
	fs.IntVar(&s.pools, "pools", 0, "pool operators at genesis, spread over the pool profiles; on a live run the first --genesis-pools operate the genesis pools")
}

func (s *simFlags) config() (engine.Config, error) {
	fitted, cohorts, err := s.population()
	if err != nil {
		return engine.Config{}, err
	}
	reg, err := s.scaled(fitted)
	if err != nil {
		return engine.Config{}, err
	}
	slots, err := sched.NewSlots(sched.Time(s.slotMs), s.epochSlots)
	if err != nil {
		return engine.Config{}, err
	}
	mistakes, err := s.mistakesOn(fitted)
	if err != nil {
		return engine.Config{}, err
	}
	pp := fakechain.DefaultParams()
	bp := build.ParamsFromConway(&pp, 0)
	// The fake chain's slot 0 is the Unix epoch.
	bp.SystemStart, bp.SlotLength = time.UnixMilli(0).UTC(), time.Duration(s.slotMs)*time.Millisecond
	return engine.Config{
		Seed: engine.SeedFromInt(s.seed), Profiles: reg, Fitted: fitted, Scale: s.scale, Cohorts: cohorts, Slots: slots,
		K: s.k, BlockEvery: s.blockEvery, TTL: s.ttl, BinaryVersion: version, Build: bp,
		NOpt: uint64(pp.NOpt), GovLifetime: pp.GovActionValidityPeriod, SimulatedPods: true,
		Floor: policy.Config{Floor: s.floor, Margin: 1},
		// The fake chain's genesis registers a pool for every operator and
		// seats internal/genesis's committee, as a devnet's does.
		GenesisPools: s.pools, GenesisPoolStake: genesis.Default().PoolStake, Committee: genesis.Default().Committee,
		Mistakes: mistakes, Run: s.run(mistakes),
	}, nil
}

// mistakesOn resolves --mistakes. Unset, mistakes are on when some
// profile's competence is a Beta with B > 0, which falls below 1; an actor
// of competence 1 never errs anyway.
func (s *simFlags) mistakesOn(reg *profile.Registry) (bool, error) {
	switch s.mistakes {
	case "on":
		return true, nil
	case "off":
		return false, nil
	case "":
		for _, n := range reg.Names() {
			if p, _ := reg.Get(n); p.Traits.Competence.B > 0 {
				return true, nil
			}
		}
		return false, nil
	}
	return false, fmt.Errorf("--mistakes %q: want on or off", s.mistakes)
}

// run names the run in findings' replay commands: the log as run --out
// writes it, and every flag that fixes the configuration, so the command
// replays it from the run's directory. It is built from the parsed values,
// so run and replay give the same one however the flags were spelled.
func (s *simFlags) run(mistakes bool) oracle.Run {
	onOff := map[bool]string{true: "on", false: "off"}
	flags := []string{
		"--wallets=" + strconv.Itoa(s.wallets), "--profiles=" + s.profiles,
		"--scale=" + strconv.FormatFloat(s.scale, 'g', -1, 64), "--slot-ms=" + strconv.FormatInt(s.slotMs, 10),
		"--epoch-slots=" + strconv.FormatUint(s.epochSlots, 10), "--k=" + strconv.FormatUint(s.k, 10),
		"--block-every=" + strconv.FormatUint(s.blockEvery, 10), "--ttl=" + strconv.FormatUint(s.ttl, 10),
		"--floor=" + strconv.FormatUint(s.floor, 10), "--pools=" + strconv.Itoa(s.pools), "--mistakes=" + onOff[mistakes],
	}
	return oracle.Run{Seed: s.seed, Log: "events.log", Flags: flags}
}

// scaled applies --scale to the population's base rates.
func (s *simFlags) scaled(reg *profile.Registry) (*profile.Registry, error) {
	if s.scale == 1 {
		return reg, nil
	}
	return reg.Scale(s.scale)
}

// spread divides n actors evenly over the profiles names.
func spread(names []string, n int) []engine.Cohort {
	var out []engine.Cohort
	if len(names) == 0 {
		return nil
	}
	for i, name := range names {
		c := n / len(names)
		if i < n%len(names) {
			c++
		}
		out = append(out, engine.Cohort{Profile: name, Count: c})
	}
	return out
}

// population reads the profile set, spreads the pools evenly over it and the
// wallets by profile weight.
func (s *simFlags) population() (*profile.Registry, []engine.Cohort, error) {
	raw, name, err := resolveProfiles(s.profiles)
	if err != nil {
		return nil, nil, err
	}
	ps, err := profile.ParseNamed(name, bytes.NewReader(raw))
	if err != nil {
		return nil, nil, err
	}
	reg, err := profile.NewRegistry(ps)
	if err != nil {
		return nil, nil, err
	}
	if s.wallets < 0 {
		return nil, nil, fmt.Errorf("--wallets %d", s.wallets)
	}
	if s.pools < 0 {
		return nil, nil, fmt.Errorf("--pools %d", s.pools)
	}
	// Pool profiles take the --pools actors and come first, so the first
	// pool actors are the genesis pools' operators; every other profile
	// shares the --wallets.
	var pools, others []string
	for _, n := range reg.Names() {
		if p, _ := reg.Get(n); p.Kind == profile.Pool {
			pools = append(pools, n)
		} else {
			others = append(others, n)
		}
	}
	if s.pools > 0 && len(pools) == 0 {
		return nil, nil, fmt.Errorf("--pools %d: the profile set has no pool profile", s.pools)
	}
	cohorts := spread(pools, s.pools)
	wallets, err := cohortsOf(reg, others, s.wallets)
	if err != nil {
		return nil, nil, err
	}
	return reg, append(cohorts, wallets...), nil
}

func cmdRun(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	var sf simFlags
	sf.register(fs)
	fake := fs.Bool("fake", false, "run against the in-process fake chain")
	epochs := fs.Uint64("epochs", 2, "epochs to simulate (a live run: 0 runs until stopped)")
	dir := fs.String("out", "", "directory for events.log (must not hold one)")
	var lf liveFlags
	lf.register(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if !*fake {
		if lf.node == "" {
			return errors.New("run: --fake or --node is required")
		}
		return runLive(ctx, sf, lf, *epochs)
	}
	if *dir == "" {
		return errors.New("run: --out is required")
	}
	cfg, err := sf.config()
	if err != nil {
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
	e := r.Engine()
	recs := mem.LastSeq()
	w := &writer{out: out}
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
	conserved := e.Supply() == genesis && chain.UnspentLovelace()+chain.Fees()+chain.Deposits() == onChain
	w.printf("epochs %d, actors %d (genesis %d), blocks %d, records %d\n",
		epochs, e.Actors(), len(funds), len(chain.Blocks()), recs)
	w.printf("payments: submitted %d, confirmed %d, released %d, skipped %d, pending %d\n",
		kinds[engine.KindSubmitted], kinds[engine.KindConfirmed], kinds[engine.KindReleased], kinds[engine.KindSkipped], e.Pending())
	w.printf("value conserved: %t (genesis %d lovelace, fees %d)\n", conserved, genesis, chain.Fees())
	t, err := engine.AuditLog(context.Background(), mem)
	if err != nil {
		return err
	}
	w.printf("mistakes %t: rejections %s; findings %d (fatal %d); lost %d lovelace\n",
		cfg.Mistakes, classCounts(t.Rejections), t.Findings, t.Fatal, t.Lost)
	w.printf("elapsed %s, %.0f events/s\n", elapsed.Round(time.Millisecond), float64(recs)/elapsed.Seconds())
	if w.err != nil {
		return w.err
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
	return cmdReplayWith(ctx, args, out, realReplayDeps())
}

func cmdReplayWith(ctx context.Context, args []string, out io.Writer, deps replayDeps) error {
	fs := flag.NewFlagSet("replay", flag.ContinueOnError)
	var sf simFlags
	sf.register(fs)
	var lf liveFlags
	lf.register(fs)
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
