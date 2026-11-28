package serve

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"

	"github.com/chrisguiney/cardano-continuum/internal/adversary"
	"github.com/chrisguiney/cardano-continuum/internal/cli/internal/kube"
	"github.com/chrisguiney/cardano-continuum/internal/cli/internal/serving"
	"github.com/chrisguiney/cardano-continuum/internal/engine"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/fakechain"
	"github.com/chrisguiney/cardano-continuum/internal/httpapi"
	"github.com/chrisguiney/cardano-continuum/internal/httpapi/stream"
	"github.com/chrisguiney/cardano-continuum/internal/httpapi/view"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
	"github.com/chrisguiney/cardano-continuum/internal/store"
)

const serveUsage = "usage: continuum serve --log EVENTS.LOG [--store DB] [--addr HOST:PORT] [--controls] [--control LOG] [--explorer-blocks N] [--network N [--namespace NS] [--kubeconfig PATH]]"

// Run serves the API over an event log, and ui beside it when ui is not
// nil: continuum serve passes nil, continuum-ui serve the web UI. It reads
// the log without ever repairing it, so it can follow a run that is still
// writing; --controls makes it the log's writer, which is only safe on a
// log no engine is appending to.
func Run(ctx context.Context, args []string, out io.Writer, ui http.Handler) error {
	return RunWith(ctx, args, out, kube.Open, ui)
}

// RunWith is Run over a cluster opener, so a test supplies a fake cluster.
func RunWith(ctx context.Context, args []string, out io.Writer, open kube.Opener, ui http.Handler) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	logPath := fs.String("log", "", "event log to serve (required)")
	control := fs.String("control", "", "control run log (same seed, no adversaries), for the Adversaries page's deltas")
	dbPath := fs.String("store", "", "store database, for actor names and roles")
	addr := fs.String("addr", "127.0.0.1:8088", "listen address; loopback only unless --allow-remote")
	remote := fs.Bool("allow-remote", false, "accept a non-loopback --addr; the API has no authentication")
	controls := fs.Bool("controls", false, "append POST /api/control requests to the log (the log must have no other writer)")
	network := fs.String("network", "", "ContinuumNetwork to read pool status from")
	ns := fs.String("namespace", "default", "namespace of the network and its pools")
	kubeconfig := fs.String("kubeconfig", "", "kubeconfig path (default: KUBECONFIG rules)")
	keep := fs.Int("explorer-blocks", serving.DefaultBlocks, "newest blocks the explorer keeps in full; older ones keep a compact index entry")
	slotMs := fs.Int64("slot-ms", 1000, "slot length in milliseconds, for the epoch")
	epochSlots := fs.Uint64("epoch-slots", 600, "slots per epoch, for the epoch")
	govLife := fs.Uint64("gov-lifetime", fakechain.DefaultParams().GovActionValidityPeriod, "governance action lifetime in epochs, for each proposal's last epoch")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *logPath == "" || fs.NArg() != 0 {
		return errors.New(serveUsage)
	}
	if err := serving.CheckAddr(*addr, *remote); err != nil {
		return err
	}
	slots, err := sched.NewSlots(sched.Time(*slotMs), *epochSlots)
	if err != nil {
		return err
	}
	hub := stream.NewHub(4096, 64, 256)
	cfg := view.LogSourceConfig{Projection: view.NewProjection(), Hub: hub, Slots: &slots, GovLifetime: *govLife}
	defer hub.Close()
	if *dbPath != "" {
		db, err := openExistingStore(ctx, *dbPath)
		if err != nil {
			return err
		}
		defer func() { _ = db.Close() }()
		cfg.Names = db
	}
	if *control != "" {
		m, err := measureLog(ctx, *control)
		if err != nil {
			return err
		}
		cfg.Control = &m
	}
	if *network != "" {
		cl, err := open(*kubeconfig, *ns)
		if err != nil {
			return err
		}
		cfg.Kube = kube.View{Cl: cl, Network: *network}
	}
	if *keep < 1 {
		return errors.New("--explorer-blocks must be at least 1")
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	ex := serving.NewExplorer(slots, 0, *keep, cfg.Names, cfg.Kube, cfg.Tips, nil, log)
	cfg.Projection.Observe(ex.Apply)
	return serve(ctx, serveParams{
		log:      *logPath,
		addr:     *addr,
		controls: *controls,
		cfg:      cfg,
		hub:      hub,
		explorer: ex,
		ui:       ui,
	}, out)
}

type serveParams struct {
	log, addr string
	controls  bool
	cfg       view.LogSourceConfig
	hub       *stream.Hub
	explorer  *httpapi.Explorer
	// ui serves the paths outside the API; nil leaves them 404.
	ui http.Handler
}

func serve(ctx context.Context, p serveParams, out io.Writer) error {
	tail, err := stream.NewTail(p.log, p.cfg.Projection, p.hub)
	if err != nil {
		return err
	}
	defer func() { _ = tail.Close() }()
	if _, err := tail.Poll(ctx); err != nil {
		return err
	}
	src := view.NewLogSource(p.cfg)
	hcfg := httpapi.Config{Source: src, People: view.NewPeople(src), Explorer: p.explorer, UI: p.ui, Logger: slog.New(slog.NewTextHandler(os.Stderr, nil))}
	if p.controls {
		fl, err := eventlog.Open(p.log)
		if err != nil {
			return err
		}
		defer func() { _ = fl.Close() }()
		now := func() sched.Time { return sched.Time(p.cfg.Projection.Snapshot(nil).SimTimeMs) }
		if hcfg.Controls, err = httpapi.NewLogControls(ctx, fl, now); err != nil {
			return err
		}
	}
	ln, err := net.Listen("tcp", p.addr)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(out, "listening on %s\n", ln.Addr()); err != nil {
		_ = ln.Close()
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	tailed := make(chan error, 1)
	go func() { tailed <- tail.Run(ctx) }()
	serr := httpapi.Serve(ctx, ln, httpapi.NewHandler(hcfg), hcfg.Logger)
	cancel()
	terr := <-tailed
	if errors.Is(terr, context.Canceled) {
		terr = nil
	}
	return errors.Join(serr, terr)
}

// openExistingStore opens a store that must already exist: serving must not
// create an empty database. Opening still applies pending migrations.
func openExistingStore(ctx context.Context, path string) (*store.DB, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("--store: %w", err)
	}
	return store.Open(ctx, path)
}

// measureLog folds the control run's log into its metrics.
func measureLog(ctx context.Context, path string) (adversary.Metrics, error) {
	l, err := eventlog.Open(path)
	if err != nil {
		return adversary.Metrics{}, fmt.Errorf("--control: %w", err)
	}
	m, err := engine.Measure(ctx, l)
	if err = errors.Join(err, l.Close()); err != nil {
		return adversary.Metrics{}, fmt.Errorf("--control %s: %w", path, err)
	}
	if m.AttackSteps > 0 {
		return adversary.Metrics{}, fmt.Errorf("--control %s is not a control run: it has %d attacks", path, m.AttackSteps)
	}
	return m, nil
}
