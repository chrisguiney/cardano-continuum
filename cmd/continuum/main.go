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
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/chrisguiney/cardano-continuum/internal/cli/attack"
	clibisect "github.com/chrisguiney/cardano-continuum/internal/cli/bisect"
	clibuild "github.com/chrisguiney/cardano-continuum/internal/cli/build"
	"github.com/chrisguiney/cardano-continuum/internal/cli/export"
	"github.com/chrisguiney/cardano-continuum/internal/cli/harvest"
	"github.com/chrisguiney/cardano-continuum/internal/cli/image"
	"github.com/chrisguiney/cardano-continuum/internal/cli/profiles"
	cliscenario "github.com/chrisguiney/cardano-continuum/internal/cli/scenario"
	"github.com/chrisguiney/cardano-continuum/internal/cli/serve"
	"github.com/chrisguiney/cardano-continuum/internal/cli/sim"
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
		return sim.Run(ctx, args[1:], out)
	case "replay":
		return sim.Replay(ctx, args[1:], out)
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
