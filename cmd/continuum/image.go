package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/chrisguiney/cardano-continuum/api/v1alpha1"
	"github.com/chrisguiney/cardano-continuum/internal/cli/shared/flagx"
	"github.com/chrisguiney/cardano-continuum/internal/imageset"
)

// imageCluster is the part of the Kubernetes API `image set` uses.
type imageCluster interface {
	Network(ctx context.Context, name string) (*v1alpha1.ContinuumNetwork, error)
	Pools(ctx context.Context, network string) ([]v1alpha1.ContinuumPool, error)
	Apply(ctx context.Context, c imageset.Change) error
}

// openCluster connects to the cluster named by kubeconfig (empty: the
// usual KUBECONFIG rules) and works in namespace.
type openCluster func(kubeconfig, namespace string) (imageCluster, error)

const imageUsage = "usage: continuum image set <impl>=<ref> --network N [--namespace NS] [--pools N% | --pool ID] [--tier T] [--dry-run]"

func cmdImage(ctx context.Context, args []string, out io.Writer, open openCluster) error {
	if len(args) == 0 || args[0] != "set" {
		return errors.New(imageUsage)
	}
	fs := flag.NewFlagSet("image set", flag.ContinueOnError)
	network := fs.String("network", "", "ContinuumNetwork to change (required)")
	ns := fs.String("namespace", "default", "namespace of the network and its pools")
	kubeconfig := fs.String("kubeconfig", "", "kubeconfig path (default: KUBECONFIG rules)")
	pools := fs.String("pools", "", "patch the first N% of matching pools, in poolId order, instead of the network default")
	poolID := fs.String("pool", "", "patch every pool with this PoolID instead of the network default")
	tier := fs.String("tier", "", "patch pools of this tier: forging, validating or logical")
	dry := fs.Bool("dry-run", false, "print what would change and change nothing")
	pos, err := flagx.ParseInterleaved(fs, args[1:])
	if err != nil {
		return err
	}
	if len(pos) != 1 || *network == "" {
		return errors.New(imageUsage)
	}
	impl, ref, err := imageset.ParseAssignment(pos[0])
	if err != nil {
		return err
	}
	sel := imageset.Selector{PoolID: *poolID, Tier: v1alpha1.Tier(*tier)}
	if *pools != "" {
		if sel.Percent, err = imageset.ParsePercent(*pools); err != nil {
			return fmt.Errorf("--pools: %w", err)
		}
	}
	cl, err := open(*kubeconfig, *ns)
	if err != nil {
		return err
	}
	net, err := cl.Network(ctx, *network)
	if err != nil {
		return err
	}
	list, err := cl.Pools(ctx, *network)
	if err != nil {
		return err
	}
	changes, err := imageset.Plan(net, list, impl, ref, sel)
	if err != nil {
		return err
	}
	if !*dry {
		for _, c := range changes {
			if err := cl.Apply(ctx, c); err != nil {
				return fmt.Errorf("patch %s %s: %w", c.Kind, c.Name, err)
			}
		}
	}
	return imageset.Write(out, changes, *dry)
}
