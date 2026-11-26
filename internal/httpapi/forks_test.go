package httpapi

import (
	"context"
	"testing"

	"github.com/chrisguiney/cardano-continuum/api/v1alpha1"
	"github.com/chrisguiney/cardano-continuum/internal/chain"
)

func mark(h uint64, tag byte) chain.Mark {
	var m chain.Mark
	m.Height, m.Slot = h, h*10
	m.Hash[0], m.Hash[1] = tag, byte(h)
	return m
}

// run is a chain of heights from to inclusive; blocks from fork on carry tag.
func run(from, to, fork uint64, tag byte) []chain.Mark {
	var out []chain.Mark
	for h := from; h <= to; h++ {
		t := byte('a')
		if h >= fork {
			t = tag
		}
		out = append(out, mark(h, t))
	}
	return out
}

func TestForksSameHeightDifferentHash(t *testing.T) {
	cs := []poolChain{
		{"a", run(1, 10, 99, 'a')},
		{"b", run(1, 10, 8, 'b')},
		{"c", run(1, 10, 99, 'a')},
	}
	fs := findForks(cs, 5, 2)
	if len(fs) != 1 || fs[0].Reason != ReasonDiverged || fs[0].Height != 10 || fs[0].Depth != 3 {
		t.Fatalf("forks %+v", fs)
	}
	if len(fs[0].Groups) != 2 {
		t.Fatalf("groups %+v", fs[0].Groups)
	}
}

func TestForksOneBlockSplitIsBelowThreshold(t *testing.T) {
	cs := []poolChain{{"a", run(1, 10, 99, 'a')}, {"b", run(1, 10, 10, 'b')}}
	if fs := findForks(cs, 5, 2); len(fs) != 0 {
		t.Fatalf("forks %+v", fs)
	}
}

func TestForksComparedAtCommonHeight(t *testing.T) {
	// b is two blocks behind on the same chain: agreement, not a fork.
	cs := []poolChain{{"a", run(1, 10, 99, 'a')}, {"b", run(1, 8, 99, 'a')}}
	if fs := findForks(cs, 5, 2); len(fs) != 0 {
		t.Fatalf("forks %+v", fs)
	}
}

func TestForksHeightGapBeyondK(t *testing.T) {
	cs := []poolChain{{"a", run(1, 20, 99, 'a')}, {"b", run(1, 10, 99, 'a')}}
	fs := findForks(cs, 5, 2)
	if len(fs) != 1 || fs[0].Reason != ReasonBehind {
		t.Fatalf("forks %+v", fs)
	}
	if g := fs[0].Groups; len(g) != 2 || g[0].Pools[0] != "b" {
		t.Fatalf("groups %+v", g)
	}
}

func TestLiveSourcePoolsFromKubeAndTips(t *testing.T) {
	ps := []v1alpha1.ContinuumPool{
		pool("p0", "x0", v1alpha1.CardanoNode, v1alpha1.TierForging, 0, ""),
		pool("p3", "x3", v1alpha1.Dingo, v1alpha1.TierForging, 0, ""),
	}
	ps[1].Status.Image.Digest = "sha256:d1"
	ps[1].Status.Phase = v1alpha1.PhaseSyncing
	s := NewLogSource(LogSourceConfig{
		Kube: fakeKube{pools: ps, mode: v1alpha1.TopologyMesh}, K: 5, ForkDepth: 2,
		Tips: fakeTips{"p0": run(1, 10, 99, 'a'), "p3": run(1, 10, 8, 'b')},
	})
	v, err := s.Pools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Pools) != 2 || v.Pools[1].Impl != "dingo" || v.Pools[1].Image != "sha256:d1" || v.Pools[1].Phase != "Syncing" {
		t.Fatalf("pools %+v", v.Pools)
	}
	if v.Pools[0].Tip.Height != 10 || v.Pools[0].Tip.Hash == "" || !v.Pools[0].Fork || !v.Pools[1].Fork {
		t.Fatalf("tips/forks %+v", v.Pools)
	}
	if len(v.Forks) != 1 || len(v.Topology.Edges) != 1 || !v.Topology.Nodes[1].Fork {
		t.Fatalf("view %+v", v)
	}
	if len(v.Tips) != 2 || v.Tips[0].Height != 10 {
		t.Fatalf("impl tips %+v", v.Tips)
	}
}

type fakeTips map[string][]chain.Mark

func (f fakeTips) Tips() map[string][]chain.Mark { return f }
