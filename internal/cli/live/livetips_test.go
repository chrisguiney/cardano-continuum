package live

import (
	"testing"

	"github.com/chrisguiney/cardano-continuum/api/v1alpha1"
	"github.com/chrisguiney/cardano-continuum/internal/chain"
)

func workloadPool(name, workload string, impl v1alpha1.Implementation, phase v1alpha1.PoolPhase, tier v1alpha1.Tier, dark bool) v1alpha1.ContinuumPool {
	p := pool(name, "id-"+name, phase)
	p.Namespace = "continuum"
	p.Spec.Implementation, p.Spec.Tier, p.Spec.Dark = impl, tier, dark
	p.Status.Workload = workload
	p.Status.Image.Digest = "sha256:" + name
	p.Status.Image.Build.Revisions = map[string]string{"dingo": "abc"}
	return p
}

// Only pools with a running pod are followed, at the pod's own DNS name.
func TestPodTargets(t *testing.T) {
	t.Parallel()
	ps := []v1alpha1.ContinuumPool{
		workloadPool("p0", "pool-aa", v1alpha1.CardanoNode, v1alpha1.PhaseSyncing, v1alpha1.TierForging, false),
		workloadPool("p1", "pool-bb", v1alpha1.Dingo, v1alpha1.PhasePending, v1alpha1.TierForging, false),
		workloadPool("p2", "pool-cc", v1alpha1.Dingo, v1alpha1.PhaseDark, v1alpha1.TierForging, true),
		workloadPool("p3", "", v1alpha1.Dingo, v1alpha1.PhaseSyncing, v1alpha1.TierLogical, false),
		workloadPool("p4", "pool-dd", v1alpha1.Dingo, v1alpha1.PhaseForging, v1alpha1.TierValidating, false),
	}
	got := podTargets(ps)
	if len(got) != 2 || got[0].Name != "p0" || got[1].Name != "p4" {
		t.Fatalf("targets %+v", got)
	}
	if want := "pool-aa-0.pool-aa.continuum.svc.cluster.local:3001"; got[0].Addr != want {
		t.Fatalf("addr %q, want %q", got[0].Addr, want)
	}
}

// Each followed pool becomes a branch carrying its implementation and build.
func TestBranchesCarryBuild(t *testing.T) {
	t.Parallel()
	ps := []v1alpha1.ContinuumPool{workloadPool("p3", "pool-aa", v1alpha1.Dingo, v1alpha1.PhaseSyncing, v1alpha1.TierForging, false)}
	m := chain.Mark{Height: 4}
	m.Slot = 40
	bs := branchesOf(ps, map[string][]chain.Mark{"p3": {m}, "gone": {m}})
	if len(bs) != 1 || bs[0].Node.Impl != "dingo" || bs[0].Node.Pool != "p3" || bs[0].Node.Digest != "sha256:p3" ||
		bs[0].Node.Revisions["dingo"] != "abc" || len(bs[0].Points) != 1 || bs[0].Points[0].Slot != 40 {
		t.Fatalf("branches %+v", bs)
	}
}
