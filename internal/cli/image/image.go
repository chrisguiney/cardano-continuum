package image

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/chrisguiney/cardano-continuum/api/v1alpha1"
	"github.com/chrisguiney/cardano-continuum/internal/cli/internal/flagx"
	"github.com/chrisguiney/cardano-continuum/internal/cli/internal/kube"
	"github.com/chrisguiney/cardano-continuum/internal/imageset"
)

const imageUsage = "usage: continuum image set <impl>=<ref> --network N [--namespace NS] [--pools N% | --pool ID] [--tier T] [--dry-run]"

// Run handles `continuum image set`, patching the network or its pools to a
// new node image through the cluster named by --kubeconfig.
func Run(ctx context.Context, args []string, out io.Writer) error {
	return RunWith(ctx, args, out, kube.Open)
}

// RunWith is Run over a cluster opener, so a test supplies a fake cluster.
func RunWith(ctx context.Context, args []string, out io.Writer, open kube.Opener) error {
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
