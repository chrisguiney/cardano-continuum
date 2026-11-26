// Command continuum-operator runs the ContinuumNetwork and ContinuumPool
// controllers and the keyed-pod fence release controller.
package main

import (
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/chrisguiney/cardano-continuum/api/v1alpha1"
	"github.com/chrisguiney/cardano-continuum/internal/ident"
	"github.com/chrisguiney/cardano-continuum/internal/operator"
	"github.com/chrisguiney/cardano-continuum/internal/operator/image"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	if err := run(os.Args[1:], log); err != nil {
		log.Error("operator failed", "err", err)
		os.Exit(1)
	}
}

type options struct {
	metrics, probes, registry, mirrors string
	insecure                           *bool
	leader                             bool
}

func parse(args []string) (options, error) {
	var o options
	fs := flag.NewFlagSet("continuum-operator", flag.ContinueOnError)
	fs.StringVar(&o.metrics, "metrics-bind-address", ":8080", "metrics listen address, or 0 to disable")
	fs.StringVar(&o.probes, "health-probe-bind-address", ":8081", "health probe listen address")
	fs.BoolVar(&o.leader, "leader-elect", true, "elect a leader so that one replica reconciles")
	fs.StringVar(&o.registry, "registry", "", "developer registry host[:port][/path] (default $"+image.EnvRegistry+")")
	fs.StringVar(&o.mirrors, "registry-mirror", "", "from=to registry hosts, comma separated: fetch manifests named by host from from to instead, as a pod reaches a registry pushed to as localhost:<port>")
	insecure := fs.Bool("registry-insecure", false, "the developer registry speaks plain HTTP (default $"+image.EnvInsecure+")")
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "registry-insecure" {
			o.insecure = insecure
		}
	})
	return o, nil
}

func run(args []string, log *slog.Logger) error {
	o, err := parse(args)
	if err != nil {
		return err
	}
	settings, err := image.NewSettings(o.registry, o.insecure, os.Getenv, "")
	if err != nil {
		return err
	}
	rc := settings.Resolver()
	if rc.Mirrors, err = image.ParseMirrors(o.mirrors); err != nil {
		return err
	}
	ctrl.SetLogger(logr.FromSlogHandler(log.Handler()))
	scheme := runtime.NewScheme()
	if err := errors.Join(clientgoscheme.AddToScheme(scheme), v1alpha1.AddToScheme(scheme)); err != nil {
		return fmt.Errorf("build scheme: %w", err)
	}
	cfg, err := ctrl.GetConfig()
	if err != nil {
		return fmt.Errorf("kubernetes config: %w", err)
	}
	cache, err := operator.CacheOptions()
	if err != nil {
		return err
	}
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:                 scheme,
		Metrics:                metricsserver.Options{BindAddress: o.metrics},
		HealthProbeBindAddress: o.probes,
		LeaderElection:         o.leader,
		LeaderElectionID:       "continuum-operator." + ident.APIGroup,
		Cache:                  cache,
	})
	if err != nil {
		return fmt.Errorf("create manager: %w", err)
	}
	exec, err := operator.NewPodExec(cfg)
	if err != nil {
		return err
	}
	ctx := ctrl.SetupSignalHandler()
	err = operator.Setup(ctx, mgr, operator.Options{
		Drivers: operator.Drivers(),
		Images:  image.NewResolver(rc, log),
		Exec:    exec,
		Log:     log,
	})
	if err != nil {
		return err
	}
	if err := errors.Join(mgr.AddHealthzCheck("ping", healthz.Ping), mgr.AddReadyzCheck("ping", healthz.Ping)); err != nil {
		return fmt.Errorf("add health checks: %w", err)
	}
	log.Info("operator starting", "registry", settings.Registry, "insecure", settings.Insecure, "leader_elect", o.leader)
	if err := mgr.Start(ctx); err != nil {
		return fmt.Errorf("run manager: %w", err)
	}
	return nil
}
