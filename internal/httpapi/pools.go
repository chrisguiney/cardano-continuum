package httpapi

import (
	"slices"
	"strings"

	"github.com/chrisguiney/cardano-continuum/api/v1alpha1"
)

// buildPools turns the operator's pool objects into the Pools view. It is
// pure: tips per implementation, forks, and the peer graph under mode.
func buildPools(ps []v1alpha1.ContinuumPool, mode v1alpha1.TopologyMode) PoolsView {
	ps = slices.Clone(ps)
	slices.SortFunc(ps, func(a, b v1alpha1.ContinuumPool) int { return strings.Compare(a.Name, b.Name) })
	forks := findForks(ps)
	forked := map[string]bool{}
	for _, f := range forks {
		for _, g := range f.Groups {
			for _, id := range g.Pools {
				forked[id] = true
			}
		}
	}
	v := PoolsView{Pools: []PoolRow{}, Tips: implTips(ps), Forks: forks, Topology: Topology{Mode: string(mode), Nodes: []Node{}}}
	for _, p := range ps {
		v.Pools = append(v.Pools, PoolRow{
			ID: p.Name, PoolID: p.Spec.PoolID, Impl: string(p.Spec.Implementation), Tier: string(p.Spec.Tier),
			Phase: string(p.Status.Phase), Dark: p.Spec.Dark, Tip: tipPoint(p), Image: p.Status.Image.Digest,
			Fork: forked[p.Name],
		})
		v.Topology.Nodes = append(v.Topology.Nodes, Node{
			ID: p.Name, Impl: string(p.Spec.Implementation), Tier: string(p.Spec.Tier),
			Phase: string(p.Status.Phase), Fork: forked[p.Name],
		})
	}
	v.Topology.Edges = edges(ps, mode)
	return v
}

func tipPoint(p v1alpha1.ContinuumPool) Point {
	return Point{Slot: uint64(max(p.Status.Tip.Slot, 0)), Hash: p.Status.Tip.Hash}
}

func implTips(ps []v1alpha1.ContinuumPool) []ImplTip {
	byImpl := map[string]*ImplTip{}
	for _, p := range ps {
		impl := string(p.Spec.Implementation)
		t := byImpl[impl]
		if t == nil {
			t = &ImplTip{Impl: impl}
			byImpl[impl] = t
		}
		t.Pools++
		if tp := tipPoint(p); tp.Slot > t.Slot || t.Hash == "" {
			t.Slot, t.Hash = tp.Slot, tp.Hash
		}
	}
	out := make([]ImplTip, 0, len(byImpl))
	for _, t := range byImpl {
		out = append(out, *t)
	}
	slices.SortFunc(out, func(a, b ImplTip) int { return strings.Compare(a.Impl, b.Impl) })
	return out
}

// findForks groups pools by tip slot and reports each slot where the pools
// that report a hash do not all agree.
func findForks(ps []v1alpha1.ContinuumPool) []Fork {
	bySlot := map[uint64]map[string][]string{}
	for _, p := range ps {
		tp := tipPoint(p)
		if tp.Hash == "" {
			continue
		}
		if bySlot[tp.Slot] == nil {
			bySlot[tp.Slot] = map[string][]string{}
		}
		bySlot[tp.Slot][tp.Hash] = append(bySlot[tp.Slot][tp.Hash], p.Name)
	}
	out := []Fork{}
	for slot, hashes := range bySlot {
		if len(hashes) < 2 {
			continue
		}
		f := Fork{Slot: slot}
		for h, names := range hashes {
			f.Groups = append(f.Groups, ForkGroup{Hash: h, Pools: names})
		}
		slices.SortFunc(f.Groups, func(a, b ForkGroup) int { return strings.Compare(a.Hash, b.Hash) })
		out = append(out, f)
	}
	slices.SortFunc(out, func(a, b Fork) int { return int(a.Slot) - int(b.Slot) })
	return out
}

// edges is the peer graph, one undirected edge per pair. A logical pool
// renders nothing and has no peers. Under relayed a forging pool peers only
// with the validating pools of its own PoolID and validating pools peer with
// each other, matching operator.Peers.
func edges(ps []v1alpha1.ContinuumPool, mode v1alpha1.TopologyMode) []Edge {
	out := []Edge{}
	for i, a := range ps {
		for _, b := range ps[i+1:] {
			if peers(mode, a, b) {
				out = append(out, Edge{From: a.Name, To: b.Name})
			}
		}
	}
	return out
}

func peers(mode v1alpha1.TopologyMode, a, b v1alpha1.ContinuumPool) bool {
	if a.Spec.Tier == v1alpha1.TierLogical || b.Spec.Tier == v1alpha1.TierLogical {
		return false
	}
	if mode != v1alpha1.TopologyRelayed {
		return true
	}
	keyedA, keyedB := a.Spec.Tier == v1alpha1.TierForging, b.Spec.Tier == v1alpha1.TierForging
	switch {
	case keyedA && keyedB:
		return false
	case keyedA || keyedB:
		return a.Spec.PoolID == b.Spec.PoolID
	}
	return true
}
