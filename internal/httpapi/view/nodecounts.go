package view

import "slices"

// WithCounts returns v with each node's Forged and Verified counts. counts
// answers, for a pool id (hex) and a height, how many blocks the selected
// chain holds up to that height and how many of them the pool issued
// (explorer.Index.Counts). v is not changed.
//
// Both counts are taken at the node's own tip height, so a node that trails
// the chain is not credited with blocks it has not reached: Forged is the
// blocks its keys issued on the chain, and Verified the others, which the
// node validated and adopted from other producers. A node without keys
// forged none. A node whose tip height is unknown has neither count.
func WithCounts(v NodesView, counts func(pool string, height uint64) (blocks, issued uint64)) NodesView {
	v.Nodes = slices.Clone(v.Nodes)
	for i, n := range v.Nodes {
		if n.Tip.Height == 0 {
			continue
		}
		blocks, forged := counts(n.StakePool, n.Tip.Height)
		if n.StakePool == "" {
			forged = 0
		}
		verified := blocks - min(forged, blocks)
		v.Nodes[i].Forged, v.Nodes[i].Verified = &forged, &verified
	}
	return v
}
