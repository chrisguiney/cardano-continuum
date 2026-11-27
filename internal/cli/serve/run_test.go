package serve

import (
	"context"
	"fmt"
	"io"

	"testing"

	"go.uber.org/goleak"

	"github.com/chrisguiney/cardano-continuum/internal/cli/sim"
	"github.com/chrisguiney/cardano-continuum/web"
)

func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

// run dispatches the commands the serve tests drive: a fake run to give
// serve a log, a replay of it, and serve itself.
func run(ctx context.Context, args []string, out io.Writer) error {
	switch args[0] {
	case "run":
		return sim.Run(ctx, args[1:], out)
	case "replay":
		return sim.Replay(ctx, args[1:], out)
	case "serve":
		return Run(ctx, args[1:], out, web.Handler())
	}
	return fmt.Errorf("unknown command %q", args[0])
}
