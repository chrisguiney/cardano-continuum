package operator

import (
	"slices"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/chrisguiney/cardano-continuum/api/v1alpha1"
)

// stubDriver stands in for a second implementation (dingo lands later) so
// that switch and fence tests can cross implementations.
type stubDriver struct{ impl v1alpha1.Implementation }

func (d stubDriver) Impl() v1alpha1.Implementation { return d.impl }
func (d stubDriver) Image() string                 { return "example.invalid/" + string(d.impl) + ":main" }
func (d stubDriver) DataDir() string               { return "" }
func (d stubDriver) Requests() Demand              { return Demand{CPUMillis: 100, MemoryMiB: 128} }

func (d stubDriver) Node(s NodeSpec) (Node, error) {
	return Node{
		Container: corev1.Container{Command: []string{string(d.impl)}},
		Config:    map[string]string{"node.yaml": "peers: " + string(rune('0'+len(s.Peers)))},
		Gate:      []string{"test", "-s", KeysDir + "/" + OpCert},
	}, nil
}

// testDrivers is the real registry plus a stub dingo driver.
func testDrivers() Registry {
	r := Drivers()
	r[v1alpha1.Dingo] = stubDriver{impl: v1alpha1.Dingo}
	return r
}

func network(ns string) *v1alpha1.ContinuumNetwork {
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

func hasVolumeFrom(spec corev1.PodSpec, secret string) bool {
	return slices.ContainsFunc(spec.Volumes, func(v corev1.Volume) bool {
		return v.Secret != nil && v.Secret.SecretName == secret
	})
}
