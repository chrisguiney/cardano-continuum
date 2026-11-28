package view

import (
	"context"
	"encoding/hex"
	"reflect"
	"strings"
	"testing"

	"github.com/chrisguiney/cardano-continuum/api/v1alpha1"
	"github.com/chrisguiney/cardano-continuum/internal/chain"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func pool(name, poolID string, impl v1alpha1.Implementation, tier v1alpha1.Tier, slot int64, hash string) v1alpha1.ContinuumPool {
	if hash != "" {
		var h chain.Mark
		copy(h.Hash[:], hash)
		hash = hex.EncodeToString(h.Hash[:])
	}
	return v1alpha1.ContinuumPool{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       v1alpha1.ContinuumPoolSpec{PoolID: poolID, Implementation: impl, Tier: tier},
		Status: v1alpha1.ContinuumPoolStatus{
			Phase: v1alpha1.PhaseForging,
			Tip:   v1alpha1.TipStatus{Slot: slot, Hash: hash},
		},
	}
}

func edgeSet(v NodesView) map[string]bool {
	m := map[string]bool{}
	for _, e := range v.Topology.Edges {
		m[e.From+"~"+e.To] = true
	}
	return m
}

func TestBuildNodesMeshForksAndTips(t *testing.T) {
	ps := []v1alpha1.ContinuumPool{
		pool("b", "p2", v1alpha1.CardanoNode, v1alpha1.TierForging, 100, "bb"),
		pool("a", "p1", v1alpha1.Dingo, v1alpha1.TierForging, 100, "aa"),
		pool("c", "p3", v1alpha1.Dingo, v1alpha1.TierValidating, 90, "cc"),
		pool("d", "p4", v1alpha1.Dingo, v1alpha1.TierLogical, 0, ""),
	}
	v := buildNodes(ps, v1alpha1.TopologyMesh, nodeFacts{k: 5, depth: 2})
	if len(v.Nodes) != 4 || v.Nodes[0].Name != "a" || v.Nodes[1].Name != "b" {
		t.Fatalf("rows %+v", v.Nodes)
	}
	if len(v.Forks) != 1 || v.Forks[0].Slot != 100 || len(v.Forks[0].Groups) != 2 {
		t.Fatalf("forks %+v", v.Forks)
	}
	if !v.Nodes[0].Fork || !v.Nodes[1].Fork || v.Nodes[2].Fork {
		t.Fatalf("fork marks %+v", v.Nodes)
	}
	tips := map[string]ImplTip{}
	for _, tp := range v.Tips {
		tips[tp.Impl] = tp
	}
	if tips["dingo"].Slot != 100 || tips["dingo"].Nodes != 3 || tips["cardano-node"].Slot != 100 {
		t.Fatalf("tips %+v", v.Tips)
	}
	e := edgeSet(v)
	// Mesh joins every pair of rendered pools; the logical pool renders nothing.
	if len(e) != 3 || !e["a~b"] || !e["a~c"] || !e["b~c"] {
		t.Fatalf("edges %v", e)
	}
	if len(v.Topology.Nodes) != 4 || v.Topology.Mode != "mesh" {
		t.Fatalf("topology %+v", v.Topology)
	}
}

func TestBuildNodesRelayed(t *testing.T) {
	ps := []v1alpha1.ContinuumPool{
		pool("bp1", "p1", v1alpha1.Dingo, v1alpha1.TierForging, 1, "x"),
		pool("r1", "p1", v1alpha1.Dingo, v1alpha1.TierValidating, 1, "x"),
		pool("bp2", "p2", v1alpha1.Dingo, v1alpha1.TierForging, 1, "x"),
		pool("r2", "p2", v1alpha1.Dingo, v1alpha1.TierValidating, 1, "x"),
	}
	e := edgeSet(buildNodes(ps, v1alpha1.TopologyRelayed, nodeFacts{k: 5, depth: 2}))
	// A keyed node dials only its own relays; relays dial each other.
	if len(e) != 3 || !e["bp1~r1"] || !e["bp2~r2"] || !e["r1~r2"] {
		t.Fatalf("edges %v", e)
	}
}

type fakeKube struct {
	pools []v1alpha1.ContinuumPool
	mode  v1alpha1.TopologyMode
}

func (f fakeKube) Pools(context.Context) ([]v1alpha1.ContinuumPool, error) { return f.pools, nil }
func (f fakeKube) Topology(context.Context) (v1alpha1.TopologyMode, error) { return f.mode, nil }

// TestNodeRowFacts: a row carries its slot lag behind the furthest node,
// its last own block, the stake pool whose keys it runs and the findings
// that involve it.
func TestNodeRowFacts(t *testing.T) {
	a := pool("a", "continuum-pool-0", v1alpha1.Dingo, v1alpha1.TierForging, 100, "aa")
	a.Status.LastBlock = v1alpha1.TipStatus{Slot: 90, Hash: "AB", Height: 9}
	b := pool("b", "continuum-pool-1", v1alpha1.CardanoNode, v1alpha1.TierValidating, 97, "bb")
	v := buildNodes([]v1alpha1.ContinuumPool{b, a}, v1alpha1.TopologyMesh, nodeFacts{
		keys:     map[string]string{"a": "0a"},
		findings: map[string]findingCount{"b": {all: 3, known: 2}},
	})
	ra, rb := v.Nodes[0], v.Nodes[1]
	if ra.Label != "continuum-pool-0" || ra.SlotLag != 0 || ra.StakePool != "0a" || ra.LastBlock == nil ||
		*ra.LastBlock != (Point{Slot: 90, Hash: "ab", Height: 9}) || ra.Findings != 0 {
		t.Fatalf("a %+v", ra)
	}
	if rb.SlotLag != 3 || rb.StakePool != "" || rb.LastBlock != nil || rb.Findings != 3 || rb.KnownFindings != 2 {
		t.Fatalf("b %+v", rb)
	}
}

// TestNodeKeys: a forging node runs the keys of the pool its spec names by
// hex id, or else of the issuer of the last block its own keys made; a
// relay or a logical node runs none.
func TestNodeKeys(t *testing.T) {
	hexID := strings.Repeat("ab", 28)
	var forged, older chain.Mark
	forged.Hash[0], forged.Issuer[0] = 1, 0xc1
	older.Hash[0] = 2
	withLast := func(p v1alpha1.ContinuumPool, m chain.Mark) v1alpha1.ContinuumPool {
		p.Status.LastBlock.Hash = hex.EncodeToString(m.Hash[:])
		return p
	}
	ps := []v1alpha1.ContinuumPool{
		pool("hex", strings.ToUpper(hexID), v1alpha1.Dingo, v1alpha1.TierForging, 1, "x"),
		withLast(pool("tipped", "continuum-pool-1", v1alpha1.Dingo, v1alpha1.TierForging, 1, "x"), forged),
		withLast(pool("indexed", "continuum-pool-2", v1alpha1.CardanoNode, v1alpha1.TierForging, 1, "x"), older),
		pool("fresh", "continuum-pool-3", v1alpha1.Dingo, v1alpha1.TierForging, 1, "x"),
		withLast(pool("relay", "continuum-pool-1", v1alpha1.Dingo, v1alpha1.TierValidating, 1, "x"), forged),
		pool("logical", hexID, v1alpha1.Dingo, v1alpha1.TierLogical, 0, ""),
	}
	// Only "other" follows the chain holding tipped's block.
	tips := map[string][]chain.Mark{"other": {forged}}
	issuerOf := func(hash string) (string, bool) {
		if hash == hex.EncodeToString(older.Hash[:]) {
			return strings.Repeat("cd", 28), true
		}
		return "", false
	}
	want := map[string]string{
		"hex":     hexID,
		"tipped":  hex.EncodeToString(forged.Issuer[:]),
		"indexed": strings.Repeat("cd", 28),
	}
	if got := NodeKeys(ps, tips, issuerOf); !reflect.DeepEqual(got, want) {
		t.Fatalf("keys %v, want %v", got, want)
	}
	if got := NodeKeys(ps, nil, nil); len(got) != 1 || got["hex"] != hexID {
		t.Fatalf("without tips or index: %v", got)
	}
}

// chainOf answers counts for a chain of n blocks of which the listed pool
// issued the blocks at the given heights (1-based).
func chainOf(n uint64, issued map[string][]uint64) func(string, uint64) (uint64, uint64) {
	return func(pool string, h uint64) (uint64, uint64) {
		var mine uint64
		for _, ih := range issued[pool] {
			if ih <= h && ih <= n {
				mine++
			}
		}
		return min(h, n), mine
	}
}

func rowAt(name, tier, pool string, h uint64) NodeRow {
	return NodeRow{Name: name, Tier: tier, StakePool: pool, Tip: Point{Height: h}}
}

func TestWithCounts(t *testing.T) {
	v := NodesView{Nodes: []NodeRow{
		rowAt("bp", "forging", "aa", 10),
		rowAt("idle", "forging", "bb", 10),
		rowAt("relay", "validating", "", 10),
		rowAt("behind", "forging", "aa", 4),
		rowAt("unknown", "forging", "aa", 0),
	}}
	got := WithCounts(v, chainOf(12, map[string][]uint64{"aa": {2, 5, 9, 11}}))

	want := map[string][2]*uint64{
		"bp":      {u(3), u(7)},
		"idle":    {u(0), u(10)},
		"relay":   {u(0), u(10)},
		"behind":  {u(1), u(3)},
		"unknown": {nil, nil},
	}
	for _, n := range got.Nodes {
		w := want[n.Name]
		if !eq(n.Forged, w[0]) || !eq(n.Verified, w[1]) {
			t.Errorf("%s: forged %v verified %v, want %v %v", n.Name, show(n.Forged), show(n.Verified), show(w[0]), show(w[1]))
		}
	}
	if v.Nodes[0].Forged != nil {
		t.Fatal("input view changed")
	}

	// A rollback below the node's tip removes blocks it counted: the counts
	// follow the chain, and the node's tip is then above the chain's.
	rolled := WithCounts(v, chainOf(6, map[string][]uint64{"aa": {2, 5, 9, 11}}))
	if n := rolled.Nodes[0]; *n.Forged != 2 || *n.Verified != 4 {
		t.Fatalf("after rollback forged %d verified %d", *n.Forged, *n.Verified)
	}
}

func u(n uint64) *uint64 { return &n }

func eq(a, b *uint64) bool { return a == nil && b == nil || a != nil && b != nil && *a == *b }

func show(p *uint64) any {
	if p == nil {
		return "nil"
	}
	return *p
}
