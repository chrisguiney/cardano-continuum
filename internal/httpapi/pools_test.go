package httpapi

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/chrisguiney/cardano-continuum/api/v1alpha1"
)

func pool(name, poolID string, impl v1alpha1.Implementation, tier v1alpha1.Tier, slot int64, hash string) v1alpha1.ContinuumPool {
	return v1alpha1.ContinuumPool{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       v1alpha1.ContinuumPoolSpec{PoolID: poolID, Implementation: impl, Tier: tier},
		Status: v1alpha1.ContinuumPoolStatus{
			Phase: v1alpha1.PhaseForging,
			Tip:   v1alpha1.TipStatus{Slot: slot, Hash: hash},
		},
	}
}

func edgeSet(v PoolsView) map[string]bool {
	m := map[string]bool{}
	for _, e := range v.Topology.Edges {
		m[e.From+"~"+e.To] = true
	}
	return m
}

func TestBuildPoolsMeshForksAndTips(t *testing.T) {
	ps := []v1alpha1.ContinuumPool{
		pool("b", "p2", v1alpha1.CardanoNode, v1alpha1.TierForging, 100, "bb"),
		pool("a", "p1", v1alpha1.Dingo, v1alpha1.TierForging, 100, "aa"),
		pool("c", "p3", v1alpha1.Dingo, v1alpha1.TierValidating, 90, "cc"),
		pool("d", "p4", v1alpha1.Dingo, v1alpha1.TierLogical, 0, ""),
	}
	v := buildPools(ps, v1alpha1.TopologyMesh)
	if len(v.Pools) != 4 || v.Pools[0].ID != "a" || v.Pools[1].ID != "b" {
		t.Fatalf("rows %+v", v.Pools)
	}
	if len(v.Forks) != 1 || v.Forks[0].Slot != 100 || len(v.Forks[0].Groups) != 2 {
		t.Fatalf("forks %+v", v.Forks)
	}
	if !v.Pools[0].Fork || !v.Pools[1].Fork || v.Pools[2].Fork {
		t.Fatalf("fork marks %+v", v.Pools)
	}
	tips := map[string]ImplTip{}
	for _, tp := range v.Tips {
		tips[tp.Impl] = tp
	}
	if tips["dingo"].Slot != 100 || tips["dingo"].Pools != 3 || tips["cardano-node"].Slot != 100 {
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

func TestBuildPoolsRelayed(t *testing.T) {
	ps := []v1alpha1.ContinuumPool{
		pool("bp1", "p1", v1alpha1.Dingo, v1alpha1.TierForging, 1, "x"),
		pool("r1", "p1", v1alpha1.Dingo, v1alpha1.TierValidating, 1, "x"),
		pool("bp2", "p2", v1alpha1.Dingo, v1alpha1.TierForging, 1, "x"),
		pool("r2", "p2", v1alpha1.Dingo, v1alpha1.TierValidating, 1, "x"),
	}
	e := edgeSet(buildPools(ps, v1alpha1.TopologyRelayed))
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
