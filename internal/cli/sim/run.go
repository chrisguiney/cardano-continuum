// Package sim runs a simulation on the in-process fake chain and replays its
// log.
package sim

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/chrisguiney/cardano-continuum/internal/cli/internal/simflags"
	"github.com/chrisguiney/cardano-continuum/internal/cli/live"
	"github.com/chrisguiney/cardano-continuum/internal/engine"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
	"github.com/chrisguiney/cardano-continuum/internal/store"
)

// Run handles `continuum run`: a fake-chain run, or a live run with --node.
func Run(ctx context.Context, args []string, out io.Writer) error {
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
	if err := WriteLog(ctx, path, mem); err != nil {
		return err
	}
	if runErr != nil {
		return runErr
	}
	return summarize(out, cfg, r, chain, mem, elapsed, *epochs)
}

// WriteLog writes the records of mem to an event log file at path.
func WriteLog(ctx context.Context, path string, mem *eventlog.MemLog) error {
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
