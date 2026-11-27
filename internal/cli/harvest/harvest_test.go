package harvest

import (
	"context"
	"io"
	"strings"
	"testing"

	"go.uber.org/goleak"
)

func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

func TestRequiresFlags(t *testing.T) {
	for name, cmd := range map[string]func(context.Context, []string, io.Writer) error{"harvest": Run, "vectors": Vectors} {
		err := cmd(context.Background(), nil, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "--repo") {
			t.Errorf("%s with no flags: err = %v", name, err)
		}
	}
}
