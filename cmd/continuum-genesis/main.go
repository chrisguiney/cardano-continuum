// Command continuum-genesis renders a blank Conway devnet's genesis files,
// node configuration and genesis-pool keys from the run seed into a
// directory. It runs as a Kubernetes Job (the Helm chart's genesis Job) and
// is idempotent: rerun against a directory that already holds the same
// spec, it exits 0 without touching it, so systemStart is kept.
//
// -publish-namespace NS then copies the directory's configuration into the
// ConfigMap -genesis-configmap and each pool's keys into the Secret
// -keys-secret names, in NS, through the in-cluster API (package publish).
// Publishing reads the directory, not the new render, so a rerun publishes
// the systemStart the pools already run.
//
// -override path=value (repeatable) changes one execution limit or cost
// model length of the default spec; genesis.OverridePaths lists the paths.
// An override that makes the genesis inconsistent is refused unless the
// matching -allow flag says it is deliberate: -allow-inconsistent-exunits
// for a per-transaction budget above the per-block one (the
// exunits-block-gap attack's devnet), -allow-short-cost-models for a cost
// model missing parameters its protocol version defines.
package main

import (
	"context"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/chrisguiney/cardano-continuum/internal/genesis"
	"github.com/chrisguiney/cardano-continuum/internal/genesis/publish"
	"github.com/chrisguiney/cardano-continuum/internal/keys"
	"github.com/chrisguiney/cardano-continuum/internal/rng"
)

// seedEnv names the environment variable holding the hex run seed. A flag
// would put the seed in the process list.
const seedEnv = "CONTINUUM_SEED"

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil)).With("component", "genesis")
	if err := run(os.Args[1:], log); err != nil {
		log.Error("genesis failed", "err", err)
		os.Exit(1)
	}
}

type options struct {
	spec  genesis.Spec
	out   string
	start string
	delay time.Duration
	// publish is the namespace to publish to; empty publishes nothing.
	publish string
	// names are the objects publish writes.
	names publish.Names
}

func parse(args []string) (options, error) {
	o := options{spec: genesis.Default()}
	s := &o.spec
	fs := flag.NewFlagSet("continuum-genesis", flag.ContinueOnError)
	fs.StringVar(&o.out, "out", "", "output directory (required)")
	fs.StringVar(&o.start, "system-start", "", "RFC 3339 system start (default: now + -start-delay)")
	fs.DurationVar(&o.delay, "start-delay", 30*time.Second, "delay from now to system start")
	fs.StringVar(&o.publish, "publish-namespace", "", "publish the genesis to this namespace's ConfigMap and Secrets (default: publish nothing)")
	fs.StringVar(&o.names.ConfigMap, "genesis-configmap", "devnet-genesis", "the ConfigMap -publish-namespace writes the configuration to")
	fs.StringVar(&o.names.Keys, "keys-secret", "keys-%d", "fmt pattern of the Secret -publish-namespace writes pool i's keys to")
	magic := fs.Uint("magic", uint(s.Magic), "network magic")
	fs.Int64Var(&s.EpochLength, "epoch-length", s.EpochLength, "epoch length in slots")
	fs.DurationVar(&s.SlotLength, "slot-length", s.SlotLength, "slot length")
	fs.Int64Var(&s.SecurityParam, "k", s.SecurityParam, "security parameter")
	fs.Float64Var(&s.ActiveSlotsCoeff, "f", s.ActiveSlotsCoeff, "active slot coefficient")
	fs.IntVar(&s.Pools, "pools", s.Pools, "genesis pools")
	fs.IntVar(&s.Committee, "committee", s.Committee, "constitutional committee members")
	fs.StringVar(&s.PoolHost, "pool-host", s.PoolHost, "fmt pattern for pool i's host name")
	fs.IntVar(&s.PoolPort, "pool-port", s.PoolPort, "pool node-to-node port")
	fs.Func("override", "path=value change to the spec, repeatable: "+strings.Join(genesis.OverridePaths(), ", "), func(v string) error {
		ov, err := genesis.ParseOverride(v)
		if err == nil {
			ov.Apply(s)
		}
		return err
	})
	fs.BoolVar(&s.Allow.InconsistentExUnits, "allow-inconsistent-exunits", false, "allow maxTxExUnits above maxBlockExUnits")
	fs.BoolVar(&s.Allow.ShortCostModels, "allow-short-cost-models", false, "allow cost models shorter than the protocol version defines")
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	if o.out == "" {
		return o, errors.New("-out is required")
	}
	if strings.Count(o.names.Keys, "%d") != 1 {
		return o, fmt.Errorf("-keys-secret %q needs exactly one %%d", o.names.Keys)
	}
	if *magic == 0 || *magic > 1<<32-1 {
		return o, fmt.Errorf("-magic %d out of range", *magic)
	}
	s.Magic = uint32(*magic)
	// Refuse an inconsistent spec at the flags, before the seed is read.
	if err := genesis.Check(*s); err != nil {
		return o, err
	}
	return o, nil
}

func run(args []string, log *slog.Logger) error {
	o, err := parse(args)
	if err != nil {
		return err
	}
	seed, err := readSeed(os.Getenv(seedEnv))
	if err != nil {
		return err
	}
	o.spec.SystemStart = time.Now().Add(o.delay).Truncate(time.Second).UTC()
	if o.start != "" {
		if o.spec.SystemStart, err = time.Parse(time.RFC3339, o.start); err != nil {
			return fmt.Errorf("-system-start: %w", err)
		}
	}
	k, err := keys.New(seed, 0)
	if err != nil {
		return err
	}
	b, err := genesis.Render(o.spec, k)
	if err != nil {
		return err
	}
	wrote, err := genesis.WriteDir(o.out, b)
	if err != nil {
		return err
	}
	if wrote {
		log.Info("genesis written", "out", o.out, "spec_hash", b.SpecHash,
			"byron_hash", b.Hashes.Byron, "shelley_hash", b.Hashes.Shelley,
			"alonzo_hash", b.Hashes.Alonzo, "conway_hash", b.Hashes.Conway,
			"faucet", b.Faucet.String(), "pools", len(b.Pools))
	} else {
		log.Info("genesis already present", "out", o.out, "spec_hash", b.SpecHash)
	}

	if o.publish == "" {
		return nil
	}
	return publishDir(o, log)
}

// publishDir publishes o.out through the in-cluster API.
func publishDir(o options, log *slog.Logger) error {
	objs, err := publish.Load(o.out, o.spec.Pools, o.publish, o.names)
	if err != nil {
		return err
	}

	cfg, err := rest.InClusterConfig()
	if err != nil {
		return fmt.Errorf("in-cluster config: %w", err)
	}

	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("kubernetes client: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := publish.Apply(ctx, cs, objs); err != nil {
		return err
	}
	log.Info("genesis published", "namespace", o.publish, "configmap", o.names.ConfigMap, "pools", len(objs.Keys))
	return nil
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
