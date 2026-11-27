package ui

import (
	"context"
	"io"

	"github.com/chrisguiney/cardano-continuum/internal/cli/serve"
	"github.com/chrisguiney/cardano-continuum/web"
)

// Serve is continuum serve with the web UI beside the API: it takes the
// same flags and serves a recorded or still-running event log.
func Serve(ctx context.Context, args []string, out io.Writer) error {
	return serve.Run(ctx, args, out, web.Handler())
}
