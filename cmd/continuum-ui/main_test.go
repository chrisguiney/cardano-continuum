package main

import (
	"context"
	"errors"
	"io"
	"testing"

	"go.uber.org/goleak"
)

func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

func TestUsageErrors(t *testing.T) {
	for _, args := range [][]string{nil, {"nosuch"}, {"run"}} {
		if err := run(context.Background(), args, io.Discard); !errors.Is(err, errUsage) {
			t.Errorf("run(%v) = %v, want usage error", args, err)
		}
	}
}
