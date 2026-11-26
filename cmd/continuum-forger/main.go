// Command continuum-forger is a stake pool that breaks consensus rules on
// purpose, for testing how honest nodes on a devnet react
// (internal/forger). With no behaviour file it is an honest pool.
//
//	continuum-forger run --genesis DIR [--keys DIR] [--peer HOST:PORT ...]
//	continuum-forger gate --keys DIR
//
// run follows the first reachable peer's chain, forges with the keys in
// --keys and serves its chain to every node that connects. gate exits 0
// only when the keys directory holds usable keys; the operator runs it as
// the pod's init container.
package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/chrisguiney/cardano-continuum/internal/forger"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, os.Args[1:], log)
	stop()
	if err != nil {
		log.Error("continuum-forger failed", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, log *slog.Logger) error {
	if len(args) == 0 {
		return errors.New("usage: continuum-forger run|gate [flags]")
	}
	switch args[0] {
	case "run":
		return cmdRun(ctx, args[1:], log)
	case "gate":
		return cmdGate(args[1:])
	}
	return fmt.Errorf("unknown command %q: want run or gate", args[0])
}

func cmdGate(args []string) error {
	fs := flag.NewFlagSet("gate", flag.ContinueOnError)
	keys := fs.String("keys", "", "keys directory (required)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *keys == "" {
		return errors.New("gate: --keys is required")
	}
	if _, err := forger.LoadCredentials(*keys); err != nil {
		return fmt.Errorf("gate: %w", err)
	}
	return nil
}

type peerList []string

func (p *peerList) String() string     { return fmt.Sprint(*p) }
func (p *peerList) Set(v string) error { *p = append(*p, v); return nil }

type runFlags struct {
	genesis, keys, listen, evidence string
	peers                           peerList
}

func cmdRun(ctx context.Context, args []string, log *slog.Logger) error {
	var f runFlags
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.StringVar(&f.genesis, "genesis", "", "directory holding config.json and the genesis files (required)")
	fs.StringVar(&f.keys, "keys", "", "keys directory; without it the forger only follows and serves")
	fs.StringVar(&f.listen, "listen", ":3001", "node-to-node listen address")
	fs.StringVar(&f.evidence, "evidence", "", "directory for the CBOR of every forged block")
	fs.Var(&f.peers, "peer", "host:port of a node to follow; repeat for fallbacks")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if f.genesis == "" {
		return errors.New("run: --genesis is required")
	}
	cfg, err := load(f)
	if err != nil {
		return fmt.Errorf("run: %w", err)
	}
	if f.evidence != "" {
		if err := os.MkdirAll(f.evidence, 0o755); err != nil {
			return fmt.Errorf("run: create evidence directory: %w", err)
		}
	}
	if cfg.Listener, err = net.Listen("tcp", f.listen); err != nil {
		return fmt.Errorf("run: listen on %s: %w", f.listen, err)
	}
	n, err := forger.New(cfg, log)
	if err != nil {
		_ = cfg.Listener.Close()
		return fmt.Errorf("run: %w", err)
	}
	p := cfg.Params
	log.Info("forger starting", "listen", cfg.Listener.Addr().String(), "magic", p.Magic,
		"genesis_hash", hex.EncodeToString(p.GenesisHash[:]), "system_start", p.SystemStart,
		"pool_stake", p.PoolStake, "total_stake", p.TotalStake, "keys", cfg.Creds,
		"peers", []string(f.peers), "seed", hex.EncodeToString(cfg.Schedule.Seed[:]), "rules", cfg.Schedule.Rules)
	return n.Run(ctx)
}

// load reads the keys, the genesis they belong to and the scenario's
// schedule. A keys directory without a behaviour file is an honest pool.
func load(f runFlags) (forger.Config, error) {
	cfg := forger.Config{Upstreams: f.peers, EvidenceDir: f.evidence}
	var pool [28]byte
	if f.keys != "" {
		c, err := forger.LoadCredentials(f.keys)
		if err != nil {
			return forger.Config{}, err
		}
		cfg.Creds, pool = c, c.PoolID
		if cfg.Schedule, err = loadSchedule(filepath.Join(f.keys, forger.BehaviourFile)); err != nil {
			return forger.Config{}, err
		}
	}
	var err error
	if cfg.Params, err = forger.LoadGenesis(f.genesis, pool); err != nil {
		return forger.Config{}, err
	}
	return cfg, nil
}

// maxSchedule bounds the behaviour file read.
const maxSchedule = 64 << 10

func loadSchedule(path string) (forger.Schedule, error) {
	st, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return forger.Schedule{}, nil
	}
	if err != nil {
		return forger.Schedule{}, fmt.Errorf("read schedule: %w", err)
	}
	if st.Size() > maxSchedule {
		return forger.Schedule{}, fmt.Errorf("schedule %s is %d bytes, over the %d byte limit", path, st.Size(), maxSchedule)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return forger.Schedule{}, fmt.Errorf("read schedule: %w", err)
	}
	var s forger.Schedule
	if err := json.Unmarshal(raw, &s); err != nil {
		return forger.Schedule{}, fmt.Errorf("parse schedule %s: %w", path, err)
	}
	return s, nil
}
