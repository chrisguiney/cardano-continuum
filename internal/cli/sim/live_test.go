package sim

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestRunNeedsFakeOrNode(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	err := run(context.Background(), []string{"run", "--out", t.TempDir()}, &out)
	if err == nil || !strings.Contains(err.Error(), "--node") {
		t.Fatalf("run without --fake or --node: %v", err)
	}
	err = run(context.Background(), []string{"run", "--node", "pool-0:3002"}, &out)
	if err == nil || !strings.Contains(err.Error(), "--state") {
		t.Fatalf("live run without --state: %v", err)
	}
}
