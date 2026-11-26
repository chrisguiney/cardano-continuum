package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"os"

	"github.com/chrisguiney/cardano-continuum/api/v1alpha1"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/httpapi"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
	"github.com/chrisguiney/cardano-continuum/internal/store"
)

const serveUsage = "usage: continuum serve --log EVENTS.LOG [--store DB] [--addr HOST:PORT] [--controls] [--network N [--namespace NS] [--kubeconfig PATH]]"

// cmdServe serves the web UI's API over an event log. It reads the log
// without ever repairing it, so it can follow a run that is still writing;
// --controls makes it the log's writer, which is only safe on a log no
// engine is appending to.
func cmdServe(ctx context.Context, args []string, out io.Writer, open openCluster) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	logPath := fs.String("log", "", "event log to serve (required)")
	dbPath := fs.String("store", "", "store database, for actor names and roles")
	addr := fs.String("addr", "127.0.0.1:8088", "listen address; loopback only unless --allow-remote")
	remote := fs.Bool("allow-remote", false, "accept a non-loopback --addr; the API has no authentication")
	controls := fs.Bool("controls", false, "append POST /api/control requests to the log (the log must have no other writer)")
	network := fs.String("network", "", "ContinuumNetwork to read pool status from")
	ns := fs.String("namespace", "default", "namespace of the network and its pools")
	kubeconfig := fs.String("kubeconfig", "", "kubeconfig path (default: KUBECONFIG rules)")
	slotMs := fs.Int64("slot-ms", 1000, "slot length in milliseconds, for the epoch")
	epochSlots := fs.Uint64("epoch-slots", 600, "slots per epoch, for the epoch")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *logPath == "" || fs.NArg() != 0 {
		return errors.New(serveUsage)
	}
	if err := checkAddr(*addr, *remote); err != nil {
		return err
	}
	slots, err := sched.NewSlots(sched.Time(*slotMs), *epochSlots)
	if err != nil {
		return err
	}
	cfg := httpapi.LogSourceConfig{Projection: httpapi.NewProjection(), Hub: httpapi.NewHub(4096, 64, 256), Slots: &slots}
	defer cfg.Hub.Close()
	if *dbPath != "" {
		db, err := openExistingStore(ctx, *dbPath)
		if err != nil {
			return err
		}
		defer func() { _ = db.Close() }()
		cfg.Names = db
	}
	if *network != "" {
		cl, err := open(*kubeconfig, *ns)
		if err != nil {
			return err
		}
		cfg.Kube = kubeView{cl: cl, network: *network}
	}
	return serve(ctx, serveParams{log: *logPath, addr: *addr, controls: *controls, cfg: cfg}, out)
}

type serveParams struct {
	log, addr string
	controls  bool
	cfg       httpapi.LogSourceConfig
}

func serve(ctx context.Context, p serveParams, out io.Writer) error {
	tail, err := httpapi.NewTail(p.log, p.cfg.Projection, p.cfg.Hub)
	if err != nil {
		return err
	}
	defer func() { _ = tail.Close() }()
	if _, err := tail.Poll(ctx); err != nil {
		return err
	}
	hcfg := httpapi.Config{Source: httpapi.NewLogSource(p.cfg), Logger: slog.New(slog.NewTextHandler(os.Stderr, nil))}
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

// checkAddr refuses a listen address that is not loopback unless the
// operator asked for it: the API has no authentication.
func checkAddr(addr string, allowRemote bool) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("--addr: %w", err)
	}
	if allowRemote || host == "localhost" {
		return nil
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || !ip.IsLoopback() {
		return fmt.Errorf("--addr %s is not loopback; the API has no authentication (use --allow-remote to override)", addr)
	}
	return nil
}

// openExistingStore opens a store that must already exist: serving must not
// create an empty database. Opening still applies pending migrations.
func openExistingStore(ctx context.Context, path string) (*store.DB, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("--store: %w", err)
	}
	return store.Open(ctx, path)
}

// kubeView adapts the image command's cluster client to httpapi.Kube.
type kubeView struct {
	cl      imageCluster
	network string
}

func (k kubeView) Topology(ctx context.Context) (v1alpha1.TopologyMode, error) {
	n, err := k.cl.Network(ctx, k.network)
	if err != nil {
		return "", err
	}
	return n.Spec.Topology.Policy, nil
}

func (k kubeView) Pools(ctx context.Context) ([]v1alpha1.ContinuumPool, error) {
	return k.cl.Pools(ctx, k.network)
}
