package kube

import (
	"context"

	"github.com/chrisguiney/cardano-continuum/api/v1alpha1"
)

// View adapts a Client to view.Kube for one network.
type View struct {
	Cl      Client
	Network string
}

// Topology returns the network's topology policy.
func (k View) Topology(ctx context.Context) (v1alpha1.TopologyMode, error) {
	n, err := k.Cl.Network(ctx, k.Network)
	if err != nil {
		return "", err
	}
	return n.Spec.Topology.Policy, nil
}

// Pools lists the network's ContinuumPools.
func (k View) Pools(ctx context.Context) ([]v1alpha1.ContinuumPool, error) {
	return k.Cl.Pools(ctx, k.Network)
}
