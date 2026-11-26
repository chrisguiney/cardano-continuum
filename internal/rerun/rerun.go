// Package rerun replays a recorded live run against a different node build:
// the same seed and profile, a devnet whose given implementation runs a new
// image. The orchestration is a Runner so tests drive it without a cluster.
package rerun

import (
	"context"
	"errors"
	"fmt"

	"github.com/chrisguiney/cardano-continuum/api/v1alpha1"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
)

// ErrFingerprint reports a log that was recorded under a different seed,
// genesis or profile set than the re-run would use.
var ErrFingerprint = errors.New("rerun: log fingerprints do not match")

// Request is one re-run.
type Request struct {
	// Network is the ContinuumNetwork whose image changes.
	Network string
	// Impl is the implementation that takes the new image.
	Impl v1alpha1.Implementation
	// Ref is the new image reference, ideally pinned to a digest.
	Ref string
	// Seed is the root seed the log was recorded with.
	Seed uint64
}

// Runner re-runs a simulation against the image in the Request. The cluster
// implementation sets the network's image through the operator and starts
// the run; tests use a fake.
type Runner interface {
	Rerun(ctx context.Context, r Request) error
}

// Replay refuses unless the log's seed, profile and genesis fingerprints
// equal want, then hands req to run. A different image is the only thing
// allowed to differ, which keeps a divergence attributable to the build.
func Replay(ctx context.Context, log, want eventlog.HeaderBody, req Request, run Runner) error {
	if req.Network == "" || req.Impl == "" || req.Ref == "" {
		return errors.New("rerun: a network, an implementation and an image are required")
	}
	if err := Check(log, want); err != nil {
		return err
	}
	if err := run.Rerun(ctx, req); err != nil {
		return fmt.Errorf("rerun %s on %s: %w", req.Impl, req.Network, err)
	}
	return nil
}

// Check compares the fingerprints of two headers.
func Check(log, want eventlog.HeaderBody) error {
	for _, f := range []struct {
		name      string
		got, want []byte
	}{
		{"seed", log.SeedFingerprint, want.SeedFingerprint},
		{"profile", log.ProfileHash, want.ProfileHash},
		{"genesis", log.GenesisHash, want.GenesisHash},
	} {
		if string(f.got) != string(f.want) {
			return fmt.Errorf("%w: %s", ErrFingerprint, f.name)
		}
	}
	return nil
}

// HeaderOf reads the header record, which is the first record of a log.
func HeaderOf(ctx context.Context, r eventlog.Reader) (eventlog.HeaderBody, error) {
	var h eventlog.HeaderBody
	found := false
	err := r.Scan(ctx, 1, func(rec eventlog.Record) error {
		if rec.Class == eventlog.Header {
			found = true
			if err := eventlog.Decode(rec.Body, &h); err != nil {
				return err
			}
		}
		return eventlog.ErrStop
	})
	if err != nil {
		return h, fmt.Errorf("read log header: %w", err)
	}
	if !found {
		return h, errors.New("rerun: log has no header")
	}
	return h, nil
}
