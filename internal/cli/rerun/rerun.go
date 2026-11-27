// Package rerun handles `replay --image`: re-running a live log on the devnet
// with one implementation's image replaced.
package rerun

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/chrisguiney/cardano-continuum/internal/cli/internal/kube"
	"github.com/chrisguiney/cardano-continuum/internal/cli/internal/simflags"
	"github.com/chrisguiney/cardano-continuum/internal/cli/live"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/imageset"
	irerun "github.com/chrisguiney/cardano-continuum/internal/rerun"
)

// Deps are the outside world a live `replay --image` touches.
type Deps struct {
	Open kube.Opener
	// Want returns the header the live re-run would record, from the
	// devnet's genesis and protocol parameters.
	Want func(ctx context.Context, sf simflags.Flags, lf live.Flags) (eventlog.HeaderBody, error)
}

// Real is the Deps a command line run uses.
func Real() Deps { return Deps{Open: kube.Open, Want: live.Header} }

// Image handles `replay --image impl=ref LOG`. A fake-chain log has no node
// build to swap, so it only says so. A live log is re-run on the devnet with
// the implementation's image replaced, after the log's seed, profile and
// genesis fingerprints are checked against the devnet's.
func Image(ctx context.Context, assign, logPath string, liveLog bool, sf simflags.Flags, lf live.Flags, out io.Writer, deps Deps) error {
	impl, ref, err := imageset.ParseAssignment(assign)
	if err != nil {
		return fmt.Errorf("replay --image: %w", err)
	}
	if !liveLog {
		_, err := fmt.Fprintf(out, "replay --image %s: a fake-chain log has no node build, so there is nothing to swap; pass --live for a live log\n", assign)
		return err
	}
	if lf.KubeNetwork == "" {
		return errors.New("replay --image --live: --network is required")
	}
	l, err := eventlog.Open(logPath)
	if err != nil {
		return err
	}
	defer func() { _ = l.Close() }()
	got, err := irerun.HeaderOf(ctx, l)
	if err != nil {
		return err
	}
	want, err := deps.Want(ctx, sf, lf)
	if err != nil {
		return err
	}
	cl, err := deps.Open(lf.Kubeconfig, lf.Namespace)
	if err != nil {
		return err
	}
	req := irerun.Request{Network: lf.KubeNetwork, Impl: impl, Ref: ref, Seed: sf.Seed}
	return irerun.Replay(ctx, got, want, req, clusterRunner{cl: cl, out: out})
}

// clusterRunner sets the network's default image through the operator's
// ContinuumNetwork field, which rolls the pods running that implementation.
type clusterRunner struct {
	cl  kube.Client
	out io.Writer
}

// Rerun implements irerun.Runner.
func (c clusterRunner) Rerun(ctx context.Context, r irerun.Request) error {
	net, err := c.cl.Network(ctx, r.Network)
	if err != nil {
		return err
	}
	pools, err := c.cl.Pools(ctx, r.Network)
	if err != nil {
		return err
	}
	changes, err := imageset.Plan(net, pools, r.Impl, r.Ref, imageset.Selector{})
	if err != nil {
		return err
	}
	for _, ch := range changes {
		if err := c.cl.Apply(ctx, ch); err != nil {
			return fmt.Errorf("patch %s %s: %w", ch.Kind, ch.Name, err)
		}
	}
	if err := imageset.Write(c.out, changes, false); err != nil {
		return err
	}
	_, err = fmt.Fprintf(c.out, "re-run with seed %d: start `continuum run --node ... --state NEW_DIR` once the pods roll; pools with their own image keep it\n", r.Seed)
	return err
}
