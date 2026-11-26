package main

import (
	"context"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/gouroboros/ledger/conway"

	"github.com/chrisguiney/cardano-continuum/internal/build"
	"github.com/chrisguiney/cardano-continuum/internal/chain"
	"github.com/chrisguiney/cardano-continuum/internal/engine"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/genesis"
	"github.com/chrisguiney/cardano-continuum/internal/rng"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
	"github.com/chrisguiney/cardano-continuum/internal/store"
	"github.com/chrisguiney/cardano-continuum/internal/submit"
)

// seedEnv names the environment variable holding the hex run seed, the one
// the genesis Job read: a flag would put the seed in the process list.
const seedEnv = "CONTINUUM_SEED"

// liveFlags are the flags only a run against a real node takes.
type liveFlags struct {
	node        string
	network     string
	state       string
	genesis     string
	faucet      uint64
	refillEvery uint64
	report      time.Duration
	gpools      int
	poolStake   uint64
}

func (l *liveFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&l.node, "node", "", "node-to-client address of a live node (host:port, or a socket path with --node-network unix)")
	fs.StringVar(&l.network, "node-network", "tcp", "network of --node: tcp or unix")
	fs.StringVar(&l.state, "state", "", "directory holding the live run's events.log and state.db; the run resumes from it")
	fs.StringVar(&l.genesis, "genesis", "", "the devnet's Shelley genesis file, which the node must match")
	fs.Uint64Var(&l.faucet, "faucet", genesis.Default().FaucetLovelace, "lovelace the genesis faucet holds")
	fs.Uint64Var(&l.refillEvery, "refill-every", 20, "slots between faucet refill rounds")
	fs.DurationVar(&l.report, "report-every", 30*time.Second, "interval between progress records")
	fs.IntVar(&l.gpools, "genesis-pools", genesis.Default().Pools, "pools the devnet genesis registers, operated by the first --pools operators")
	fs.Uint64Var(&l.poolStake, "pool-stake", genesis.Default().PoolStake, "lovelace each genesis pool's owner holds delegated to it")
}

// liveConfig is the engine configuration of a live run: the population from
// the flags, the slot shape and k from the node's genesis, the builder from
// the node's protocol parameters, and the run seed, so the faucet is the one
// internal/genesis funded.
func liveConfig(sf simFlags, lf liveFlags, g chain.Genesis, np nodeParams, seed rng.Seed) (engine.Config, error) {
	bp := np.build
	reg, cohorts, err := sf.population()
	if err != nil {
		return engine.Config{}, err
	}
	if g.SlotLength%time.Millisecond != 0 {
		return engine.Config{}, fmt.Errorf("slot length %s is not whole milliseconds", g.SlotLength)
	}
	slots, err := sched.NewSlots(sched.Time(g.SlotLength.Milliseconds()), g.EpochLength)
	if err != nil {
		return engine.Config{}, err
	}
	bp.SystemStart, bp.SlotLength = g.Start, g.SlotLength
	// The guard needs every genesis pool's stake, so a run with operators
	// operates all of them; one without has no pools to guard.
	gpools := 0
	if sf.pools > 0 {
		if sf.pools < lf.gpools {
			return engine.Config{}, fmt.Errorf("--pools %d cannot operate the %d genesis pools", sf.pools, lf.gpools)
		}
		gpools = lf.gpools
	}
	return engine.Config{
		Seed: seed, Profiles: reg, Cohorts: cohorts, Slots: slots, K: g.SecurityParam, TTL: sf.ttl,
		Build: bp, BinaryVersion: version, Faucet: lf.faucet, RefillEvery: lf.refillEvery,
		NOpt: np.nOpt, GovLifetime: np.govLifetime, GenesisPoolStake: lf.poolStake, GenesisPools: gpools,
	}, nil
}

func runLive(ctx context.Context, sf simFlags, lf liveFlags, epochs uint64) error {
	if lf.state == "" || lf.genesis == "" {
		return errors.New("run: a live run needs --state and --genesis")
	}
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	seed, err := readSeed(os.Getenv(seedEnv))
	if err != nil {
		return err
	}
	want, err := readGenesis(lf.genesis)
	if err != nil {
		return err
	}
	node := chain.Node{Network: lf.network, Address: lf.node, Magic: want.Magic, Log: log}
	q := chain.NewQuerier(node)
	if err := guard(ctx, log, q, want); err != nil {
		return err
	}
	np, err := queryParams(ctx, q)
	if err != nil {
		return err
	}
	cfg, err := liveConfig(sf, lf, want, np, seed)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(lf.state, 0o750); err != nil {
		return err
	}
	fl, err := eventlog.Open(filepath.Join(lf.state, "events.log"))
	if err != nil {
		return err
	}
	defer func() { _ = fl.Close() }()
	db, err := store.Open(ctx, filepath.Join(lf.state, "state.db"), store.WithLog(fl))
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	if fl.LastSeq() == 0 {
		if err := checkFaucet(ctx, q, cfg); err != nil {
			return err
		}
	}
	dial := func(ctx context.Context) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, lf.network, lf.node)
	}
	live := engine.NewLive(node, submit.NewLocal(dial, want.Magic), cfg.K, log)
	live.Start(ctx)
	defer func() {
		c, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		if err := live.Close(c); err != nil {
			log.Warn("live node close", "err", err)
		}
	}()
	clock := sched.NewWall(want.Start)
	r, err := engine.Start(ctx, cfg, engine.Deps{Clock: clock, Log: fl, Store: db, Node: live, CheckpointEvery: 200, Logger: log})
	if err != nil {
		return err
	}
	end := sched.Time(math.MaxInt64)
	if epochs > 0 {
		end = cfg.Slots.EpochStart(epochs)
	}
	return follow(ctx, log, cfg, r, fl, clock, end, lf.report)
}

// follow runs the loop in report-sized stretches, logging progress after
// each; every stretch ends with a checkpoint.
func follow(ctx context.Context, log *slog.Logger, cfg engine.Config, r *engine.Runner, l engine.Log, clock sched.Clock, end sched.Time, every time.Duration) error {
	for {
		until := min(clock.Now()+sched.Time(every.Milliseconds()), end)
		err := r.Run(ctx, until)
		if rerr := report(ctx, log, cfg, r, l, clock); rerr != nil {
			return errors.Join(err, rerr)
		}
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return nil
			}
			return err
		}
		if until >= end {
			return nil
		}
	}
}

// report logs one progress record. Its keys are what the kind e2e reads.
func report(ctx context.Context, log *slog.Logger, cfg engine.Config, r *engine.Runner, l engine.Log, clock sched.Clock) error {
	t, err := engine.AuditLog(context.WithoutCancel(ctx), l)
	if err != nil {
		return err
	}
	e := r.Engine()
	now := clock.Now()
	log.Info("progress", "epoch", cfg.Slots.EpochAt(now), "slot", cfg.Slots.SlotAt(now), "tip_slot", e.Tip().Slot,
		"actors", e.Actors(), "payments", t.Payments, "refills", t.Refills, "confirmed", t.Confirmed, "paid", t.Paid,
		"released", t.Released, "double_paid", t.DoublePaid, "pending", e.Pending(),
		"supply", e.Supply(), "genesis", cfg.Faucet, "conserved", e.Supply() == cfg.Faucet, "log_seq", l.LastSeq())
	return nil
}

// guard waits for the node to answer, then refuses a public network or a
// node on another genesis than the file the run was deployed with.
func guard(ctx context.Context, log *slog.Logger, q *chain.Querier, want chain.Genesis) error {
	for {
		got, err := q.Genesis(ctx)
		if err == nil {
			return chain.Guard(want, got)
		}
		log.Warn("node not answering yet", "err", err)
		t := time.NewTimer(5 * time.Second)
		select {
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-t.C:
		}
	}
}

// nodeParams are what a live run takes from the node's protocol
// parameters: the builder's, so fees and min-UTxO match the ledger that
// judges them, and the view's nOpt and governance action lifetime.
type nodeParams struct {
	build             build.Params
	nOpt, govLifetime uint64
}

func queryParams(ctx context.Context, q *chain.Querier) (nodeParams, error) {
	pp, err := q.Params(ctx)
	if err != nil {
		return nodeParams{}, err
	}
	cp, ok := pp.(*conway.ConwayProtocolParameters)
	if !ok {
		return nodeParams{}, fmt.Errorf("node is not in Conway: protocol parameters are %T", pp)
	}
	return nodeParams{build: build.ParamsFromConway(cp, 0), nOpt: uint64(cp.NOpt), govLifetime: cp.GovActionValidityPeriod}, nil
}

// checkFaucet refuses a fresh run whose faucet does not hold its genesis
// output on the node: a wrong seed or faucet amount would otherwise show up
// only as every refill rejected.
func checkFaucet(ctx context.Context, q *chain.Querier, cfg engine.Config) error {
	funds, err := cfg.Genesis()
	if err != nil {
		return err
	}
	f := funds[0]
	addr, err := f.Address.Bytes()
	if err != nil {
		return err
	}
	us, err := q.UtxosAt(ctx, []common.Address{f.Address})
	if err != nil {
		return err
	}
	ref := common.Blake2b256Hash(addr)
	for _, u := range us {
		if u.Id.Id() == ref && u.Id.Index() == 0 && u.Output.Amount().Uint64() == f.Lovelace {
			return nil
		}
	}
	return fmt.Errorf("faucet %s has no genesis output of %d lovelace on the node: check the seed and --faucet", f.Address, f.Lovelace)
}

func readGenesis(path string) (chain.Genesis, error) {
	f, err := os.Open(path)
	if err != nil {
		return chain.Genesis{}, err
	}
	defer func() { _ = f.Close() }()
	raw, err := io.ReadAll(io.LimitReader(f, 1<<20+1))
	if err != nil {
		return chain.Genesis{}, fmt.Errorf("read %s: %w", path, err)
	}
	return chain.ParseShelleyGenesis(raw)
}

func readSeed(h string) (rng.Seed, error) {
	var s rng.Seed
	raw, err := hex.DecodeString(h)
	if err != nil || len(raw) != len(s) {
		// The value is secret: report only its shape.
		return s, fmt.Errorf("%s must be %d hex-encoded bytes (got %d characters)", seedEnv, len(s), len(h))
	}
	copy(s[:], raw)
	return s, nil
}
