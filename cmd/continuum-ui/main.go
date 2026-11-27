// Command continuum-ui serves the web UI, apart from the engine binary so a
// UI change never changes the engine image (and so never resets a run).
//
//	continuum-ui proxy --api http://HOST:PORT [--addr 127.0.0.1:8088] [--allow-remote]
//	continuum-ui serve --log EVENTS.LOG [continuum serve's flags]
//
// proxy serves the UI and forwards /api/* to an engine's live API
// (continuum run --serve); the chart runs it in front of the engine. serve
// is continuum serve with the UI beside its API, for viewing a recorded run.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/chrisguiney/cardano-continuum/internal/cli/ui"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, os.Args[1:], os.Stdout)
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, "continuum-ui:", err)
		os.Exit(1)
	}
}

var errUsage = errors.New("usage: continuum-ui proxy|serve [flags]")

func run(ctx context.Context, args []string, out io.Writer) error {
	if len(args) == 0 {
		return errUsage
	}

	switch args[0] {
	case "proxy":
		return ui.Proxy(ctx, args[1:], out)
	case "serve":
		return ui.Serve(ctx, args[1:], out)
	}
	return fmt.Errorf("%w: unknown command %q", errUsage, args[0])
}
