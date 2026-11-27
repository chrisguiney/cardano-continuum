package export

import (
	"context"
	"fmt"
	"io"
	"testing"

	"go.uber.org/goleak"

	"github.com/chrisguiney/cardano-continuum/internal/cli/sim"
)

func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

// run dispatches the commands these tests drive, as the binary does.
func run(ctx context.Context, args []string, out io.Writer) error {
	switch args[0] {
	case "run":
		return sim.Run(ctx, args[1:], out)
	case "export":
		return Run(ctx, args[1:], out)
	}
	return fmt.Errorf("unknown command %q", args[0])
}
