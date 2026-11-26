package pool

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/chrisguiney/cardano-continuum/api/v1alpha1"
)

func testNetwork(ns string) *v1alpha1.ContinuumNetwork {
	return &v1alpha1.ContinuumNetwork{
		ObjectMeta: metav1.ObjectMeta{Name: "net", Namespace: ns},
		Spec: v1alpha1.ContinuumNetworkSpec{
			Magic:            42,
			GenesisRef:       "genesis",
			Topology:         v1alpha1.TopologyPolicy{Policy: v1alpha1.TopologyMesh},
			Budget:           v1alpha1.BudgetSpec{MaxPools: 10, CPUMillis: 10000, MemoryMiB: 65536},
			EpochLength:      500,
			SecurityParam:    10,
			ImagePullSecrets: []string{"pull"},
		},
	}
}

func pool(ns, name string, impl v1alpha1.Implementation, tier v1alpha1.Tier) *v1alpha1.ContinuumPool {
	p := &v1alpha1.ContinuumPool{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, UID: "uid-" + "0000"},
		Spec: v1alpha1.ContinuumPoolSpec{
			NetworkRef:     "net",
			PoolID:         "pool1abc",
			Implementation: impl,
			Tier:           tier,
		},
	}
	if tier == v1alpha1.TierForging {
		p.Spec.KeysSecret = name + "-keys"
	}
	return p
}
