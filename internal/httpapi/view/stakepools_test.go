package view

import (
	"strings"
	"testing"

	"github.com/chrisguiney/cardano-continuum/internal/store"
)

// TestBuildStakePools: a pool actor's row carries its operator, the
// delegators whose stake points at it and its votes; a node running its
// keys links to it, and a pool no node runs has no nodes.
func TestBuildStakePools(t *testing.T) {
	a, b := strings.Repeat("0a", 28), strings.Repeat("0b", 28)
	ba, bb := PoolBech32(a), PoolBech32(b)
	in := stakeInputs{
		names: map[uint64]store.ActorRow{7: {ID: 7, Name: "pool-7", Profile: "pools"}, 8: {ID: 8, Name: "pool-8"}},
		idents: map[uint64]store.Identity{
			7:  {Kind: "pool", Keys: store.Keys{PoolID: ba}},
			8:  {Kind: "pool", Keys: store.Keys{PoolID: bb}},
			20: {Kind: "wallet", Holdings: store.Holdings{Settled: 5}, Delegation: store.Delegation{DelegatedPool: ba, DelegatedActor: 7}},
			21: {Kind: "wallet", Holdings: store.Holdings{Settled: 6}, Delegation: store.Delegation{DelegatedPool: ba}},
			22: {Kind: "wallet", Holdings: store.Holdings{Settled: 9}},
		},
		states: map[uint64]string{7: "registered", 8: "retired"},
		votes:  map[string]int{a: 2},
	}
	v := buildStakePools(in)
	if len(v.Pools) != 2 {
		t.Fatalf("rows %+v", v.Pools)
	}
	p := v.Pools[0]
	if p.ID != a || p.Bech32 != ba || p.Actor == nil || *p.Actor != 7 || p.ActorName != "pool-7" || p.Profile != "pools" ||
		p.ActorState != "registered" || p.Delegators != 2 || p.DelegatedLovelace != 11 || p.Votes != 2 || p.Blocks != nil || len(p.Nodes) != 0 || p.Nodes == nil {
		t.Fatalf("pool 7 %+v", p)
	}
	if q := v.Pools[1]; q.ID != b || q.ActorState != "retired" || q.Delegators != 0 {
		t.Fatalf("pool 8 %+v", q)
	}

	nodes := NodesView{Nodes: []NodeRow{
		{Name: "p3", Impl: "dingo", Tier: "forging", Phase: "Forging", StakePool: a},
		{Name: "r1", Impl: "dingo", Tier: "validating", Phase: "Following"},
		{Name: "p9", Impl: "cardano-node", Tier: "forging", Phase: "Forging", StakePool: strings.Repeat("0c", 28)},
	}}
	w := WithNodes(v, nodes)
	if got := w.Pools[0].Nodes; len(got) != 1 || got[0] != (NodeRef{Name: "p3", Impl: "dingo", Tier: "forging", Phase: "Forging"}) {
		t.Fatalf("pool 7 nodes %+v", got)
	}
	if len(w.Pools) != 3 || w.Pools[2].Actor != nil || len(w.Pools[2].Nodes) != 1 || w.Pools[2].Bech32 == "" {
		t.Fatalf("a node's pool no actor operates still gets a row: %+v", w.Pools)
	}
	if len(v.Pools[0].Nodes) != 0 {
		t.Fatalf("WithNodes changed its argument: %+v", v.Pools[0])
	}
}

func TestPoolBech32RoundTrip(t *testing.T) {
	h := strings.Repeat("c4", 28)
	b := PoolBech32(h)
	if !strings.HasPrefix(b, "pool1") {
		t.Fatalf("bech32 %q", b)
	}
	if got, ok := PoolHex(b); !ok || got != h {
		t.Fatalf("hex %q %v", got, ok)
	}
	if _, ok := PoolHex("addr_test1xyz"); ok {
		t.Fatal("not a pool id")
	}
}
