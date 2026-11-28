package view

import (
	"encoding/hex"
	"slices"
	"strings"

	"github.com/chrisguiney/cardano-continuum/api/v1alpha1"
	"github.com/chrisguiney/cardano-continuum/internal/chain"
)

// NodeRow is one node: a node process the operator manages (a
// ContinuumPool object), an implementation at a tier. A stake pool is an
// on-chain registration (StakePoolRow). A forging node runs one stake
// pool's keys; a stake pool may have no node at all, when it exists only on
// chain.
type NodeRow struct {
	// Name is the ContinuumPool object's name.
	Name string `json:"name"`
	// Label is spec.poolId: the fence's key, which nodes sharing a stake
	// pool's keys or relaying for it have in common. It is a stake pool id
	// only when it is 56 hex digits.
	Label string `json:"label"`
	// Impl is the node implementation.
	Impl string `json:"impl"`
	// Tier is what the operator renders: logical, validating or forging.
	Tier string `json:"tier"`
	// Phase is the operator's observed phase.
	Phase string `json:"phase"`
	// Dark reports the workload scaled to zero on purpose.
	Dark bool `json:"dark"`
	// Tip is the node's chain tip.
	Tip Point `json:"tip"`
	// SlotLag is how many slots the tip trails the furthest node's tip.
	SlotLag uint64 `json:"slot_lag"`
	// Image is the image digest the workload runs.
	Image string `json:"image,omitempty"`
	// Fork reports the node on a branch other nodes disagree with.
	Fork bool `json:"fork"`
	// LastBlock is the newest block the node's own keys made; nil when the
	// operator saw none.
	LastBlock *Point `json:"last_block"`
	// StakePool is the pool id (hex) whose keys the node runs (NodeKeys);
	// empty for a node without keys or one that has not forged yet.
	StakePool string `json:"stake_pool,omitempty"`
	// Forged counts the blocks on the selected chain, up to the node's tip,
	// that its stake pool keys issued; 0 for a node without keys. Nil when
	// the tip height or the chain index is unknown.
	Forged *uint64 `json:"forged"`
	// Verified counts the other blocks on the chain up to the node's tip:
	// those it validated and adopted from other producers. Nil as Forged.
	Verified *uint64 `json:"verified"`
	// Findings counts the oracle findings that involve the node, and
	// KnownFindings those a known upstream issue explains.
	Findings      int `json:"findings"`
	KnownFindings int `json:"known_findings"`
	// Rollbacks counts the times the node moved its selected chain back to
	// a point behind its tip, as the engine's chain-sync follower saw it
	// since it began following the node.
	Rollbacks uint64 `json:"rollbacks"`
	// Deepest is the rollback that dropped the most blocks; nil when none.
	Deepest *RollbackRow `json:"deepest_rollback"`
	// LastRollback is the newest rollback; nil when none.
	LastRollback *RollbackRow `json:"last_rollback"`
	// RecentRollbacks are the newest rollbacks, newest first; never nil.
	RecentRollbacks []RollbackRow `json:"recent_rollbacks"`
}

// ImplTip is the furthest tip among one implementation's nodes.
type ImplTip struct {
	// Impl is the implementation.
	Impl string `json:"impl"`
	// Slot, Hash and Height are the furthest tip.
	Slot   uint64 `json:"slot"`
	Hash   string `json:"hash"`
	Height uint64 `json:"height,omitempty"`
	// Nodes counts the implementation's nodes.
	Nodes int `json:"nodes"`
}

// ForkGroup is the nodes that agree on one block hash.
type ForkGroup struct {
	// Hash is the block they hold.
	Hash string `json:"hash"`
	// Nodes are their names.
	Nodes []string `json:"nodes"`
}

// Fork reasons.
const (
	// ReasonDiverged: nodes hold different blocks at the same height.
	ReasonDiverged = "diverged"
	// ReasonBehind: a node's tip is more than k blocks below the furthest.
	ReasonBehind = "behind"
)

// Fork marks nodes whose chains disagree. For ReasonDiverged, Height is
// the common height compared and Depth the number of blocks, counting
// down from it, on which the nodes differ. Height and Depth are zero when
// the source knows only slots, and the nodes are compared at Slot.
type Fork struct {
	// Slot is where the nodes are compared when heights are unknown.
	Slot uint64 `json:"slot"`
	// Height is the common height compared.
	Height uint64 `json:"height,omitempty"`
	// Depth is how many blocks down from Height the nodes differ on.
	Depth uint64 `json:"depth,omitempty"`
	// Reason is ReasonDiverged or ReasonBehind.
	Reason string `json:"reason"`
	// Groups are the sides; for ReasonBehind the laggards come first.
	Groups []ForkGroup `json:"groups"`
}

// Node is a node in the topology graph.
type Node struct {
	// ID is the node's name.
	ID string `json:"id"`
	// Impl, Tier and Phase are as in NodeRow.
	Impl  string `json:"impl"`
	Tier  string `json:"tier"`
	Phase string `json:"phase"`
	// Fork marks the node as on a disagreeing branch.
	Fork bool `json:"fork"`
}

// Edge is a peering between two nodes, From sorting before To.
type Edge struct {
	// From and To are node names.
	From string `json:"from"`
	To   string `json:"to"`
}

// Topology is the peer graph of the nodes.
type Topology struct {
	// Mode is the network's peer policy.
	Mode string `json:"mode"`
	// Nodes and Edges are the graph.
	Nodes []Node `json:"nodes"`
	Edges []Edge `json:"edges"`
}

// NodesView is the Nodes view.
type NodesView struct {
	// Nodes are the rows, by name.
	Nodes []NodeRow `json:"nodes"`
	// Tips is the furthest tip per implementation.
	Tips []ImplTip `json:"tips"`
	// Forks are the disagreements between nodes' chains.
	Forks []Fork `json:"forks"`
	// Topology is the peer graph.
	Topology Topology `json:"topology"`
}

// nodeFacts is what a NodesView takes from outside the operator's objects.
type nodeFacts struct {
	// tips are the followed chains by node name.
	tips map[string][]chain.Mark
	// k is the security parameter and depth the fork depth reported.
	k, depth uint64
	// keys are NodeKeys' answer.
	keys map[string]string
	// findings counts findings by node name.
	findings map[string]findingCount
	// rollbacks are the followers' rollback logs by node name.
	rollbacks map[string]chain.RollbackLog
}

// findingCount is how many findings involve a node, and how many of them
// a known issue explains.
type findingCount struct {
	all, known int
}

// buildNodes turns the operator's pool objects into the Nodes view. It is
// pure: tips per implementation, forks, and the peer graph under mode.
func buildNodes(ps []v1alpha1.ContinuumPool, mode v1alpha1.TopologyMode, f nodeFacts) NodesView {
	ps = slices.Clone(ps)
	slices.SortFunc(ps, func(a, b v1alpha1.ContinuumPool) int { return strings.Compare(a.Name, b.Name) })
	chains := poolChains(ps, f.tips)
	forks := findForks(chains, f.k, f.depth)
	forked := map[string]bool{}
	for _, fk := range forks {
		for _, g := range fk.Groups {
			for _, id := range g.Nodes {
				forked[id] = true
			}
		}
	}

	var top uint64
	for _, p := range ps {
		top = max(top, tipPoint(p.Name, chains).Slot)
	}

	v := NodesView{Nodes: []NodeRow{}, Tips: implTips(ps, chains), Forks: forks, Topology: Topology{Mode: string(mode), Nodes: []Node{}}}
	for _, p := range ps {
		v.Nodes = append(v.Nodes, nodeRow(p, chains, top, forked[p.Name], f))
		v.Topology.Nodes = append(v.Topology.Nodes, Node{
			ID:    p.Name,
			Impl:  string(p.Spec.Implementation),
			Tier:  string(p.Spec.Tier),
			Phase: string(p.Status.Phase),
			Fork:  forked[p.Name],
		})
	}
	v.Topology.Edges = edges(ps, mode)
	return v
}

func nodeRow(p v1alpha1.ContinuumPool, chains []poolChain, top uint64, fork bool, f nodeFacts) NodeRow {
	tip := tipPoint(p.Name, chains)
	var last *Point
	if lb := p.Status.LastBlock; lb.Hash != "" {
		last = &Point{
			Slot:   uint64(max(lb.Slot, 0)),
			Hash:   strings.ToLower(lb.Hash),
			Height: uint64(max(lb.Height, 0)),
		}
	}
	return NodeRow{
		Name:            p.Name,
		Label:           p.Spec.PoolID,
		Impl:            string(p.Spec.Implementation),
		Tier:            string(p.Spec.Tier),
		Phase:           string(p.Status.Phase),
		Dark:            p.Spec.Dark,
		Tip:             tip,
		SlotLag:         top - tip.Slot,
		Image:           p.Status.Image.Digest,
		Fork:            fork,
		LastBlock:       last,
		StakePool:       f.keys[p.Name],
		Findings:        f.findings[p.Name].all,
		KnownFindings:   f.findings[p.Name].known,
		Rollbacks:       f.rollbacks[p.Name].Count,
		Deepest:         deepestRow(f.rollbacks[p.Name]),
		LastRollback:    lastRow(f.rollbacks[p.Name]),
		RecentRollbacks: recentRows(f.rollbacks[p.Name]),
	}
}

func tipPoint(name string, cs []poolChain) Point {
	for _, c := range cs {
		if c.name == name {
			m := c.marks[len(c.marks)-1]
			return Point{Slot: m.Slot, Hash: hex.EncodeToString(m.Hash[:]), Height: m.Height}
		}
	}
	return Point{}
}

func implTips(ps []v1alpha1.ContinuumPool, cs []poolChain) []ImplTip {
	byImpl := map[string]*ImplTip{}
	for _, p := range ps {
		impl := string(p.Spec.Implementation)
		t := byImpl[impl]
		if t == nil {
			t = &ImplTip{Impl: impl}
			byImpl[impl] = t
		}
		t.Nodes++
		tp := tipPoint(p.Name, cs)
		if tp.Height > t.Height || tp.Height == t.Height && tp.Slot > t.Slot || t.Hash == "" {
			t.Slot, t.Hash, t.Height = tp.Slot, tp.Hash, tp.Height
		}
	}
	out := make([]ImplTip, 0, len(byImpl))
	for _, t := range byImpl {
		out = append(out, *t)
	}
	slices.SortFunc(out, func(a, b ImplTip) int { return strings.Compare(a.Impl, b.Impl) })
	return out
}

// edges is the peer graph, one undirected edge per pair. A logical pool
// renders nothing and has no peers. Under relayed a forging pool peers only
// with the validating pools of its own PoolID and validating pools peer with
// each other, matching render.Peers.
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

// RollbackRow is one rollback of a node's selected chain.
type RollbackRow struct {
	// From is the tip the node held when it rolled back.
	From Point `json:"from"`
	// To is the point it rolled back to; its Height is the height of From
	// minus Blocks, and zero when Beyond.
	To Point `json:"to"`
	// Blocks is the depth: the blocks dropped.
	Blocks uint64 `json:"blocks"`
	// Slots is the slots between From and To.
	Slots uint64 `json:"slots"`
	// Beyond reports To older than the blocks the follower keeps, so Blocks
	// and Slots are lower bounds.
	Beyond bool `json:"beyond"`
}

func rollbackRow(r chain.Rollback) RollbackRow {
	to := Point{Slot: r.To.Slot, Hash: hex.EncodeToString(r.To.Hash[:])}
	if !r.Beyond {
		to.Height = r.From.Height - min(r.Blocks, r.From.Height)
	}
	return RollbackRow{
		From:   Point{Slot: r.From.Slot, Hash: hex.EncodeToString(r.From.Hash[:]), Height: r.From.Height},
		To:     to,
		Blocks: r.Blocks,
		Slots:  r.Slots,
		Beyond: r.Beyond,
	}
}

func deepestRow(l chain.RollbackLog) *RollbackRow {
	if l.Count == 0 {
		return nil
	}
	r := rollbackRow(l.Deepest)
	return &r
}

func lastRow(l chain.RollbackLog) *RollbackRow {
	if l.Count == 0 {
		return nil
	}
	r := rollbackRow(l.Last)
	return &r
}

func recentRows(l chain.RollbackLog) []RollbackRow {
	out := make([]RollbackRow, 0, len(l.Recent))
	for _, r := range l.Recent {
		out = append(out, rollbackRow(r))
	}
	return out
}
