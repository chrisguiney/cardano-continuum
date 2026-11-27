package main

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/chrisguiney/cardano-continuum/internal/cli/live"
	"github.com/chrisguiney/cardano-continuum/internal/cli/shared/kube"
	"github.com/chrisguiney/cardano-continuum/internal/cli/shared/simflags"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/imageset"
	"github.com/chrisguiney/cardano-continuum/internal/rerun"
)

// replayDeps are the outside world a live `replay --image` touches.
type replayDeps struct {
	open kube.Opener
	// want returns the header the live re-run would record, from the
	// devnet's genesis and protocol parameters.
	want func(ctx context.Context, sf simflags.Flags, lf live.Flags) (eventlog.HeaderBody, error)
}

func realReplayDeps() replayDeps { return replayDeps{open: kube.Open, want: live.Header} }

// replayImage handles `replay --image impl=ref LOG`. A fake-chain log has no
// node build to swap, so it only says so. A live log is re-run on the
// devnet with the implementation's image replaced, after the log's seed,
// profile and genesis fingerprints are checked against the devnet's.
func replayImage(ctx context.Context, assign, logPath string, live bool, sf simflags.Flags, lf live.Flags, out io.Writer, deps replayDeps) error {
	impl, ref, err := imageset.ParseAssignment(assign)
	if err != nil {
		return fmt.Errorf("replay --image: %w", err)
	}
	if !live {
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
	got, err := rerun.HeaderOf(ctx, l)
	if err != nil {
		return err
	}
	want, err := deps.want(ctx, sf, lf)
	if err != nil {
		return err
	}
	cl, err := deps.open(lf.Kubeconfig, lf.Namespace)
	if err != nil {
		return err
	}
	req := rerun.Request{Network: lf.KubeNetwork, Impl: impl, Ref: ref, Seed: sf.Seed}
	return rerun.Replay(ctx, got, want, req, clusterRunner{cl: cl, out: out})
}

// clusterRunner sets the network's default image through the operator's
// ContinuumNetwork field, which rolls the pods running that implementation.
type clusterRunner struct {
	cl  kube.Client
	out io.Writer
}

// Rerun implements rerun.Runner.
func (c clusterRunner) Rerun(ctx context.Context, r rerun.Request) error {
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
