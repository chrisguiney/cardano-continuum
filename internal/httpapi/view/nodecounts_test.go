package view

import "testing"

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
