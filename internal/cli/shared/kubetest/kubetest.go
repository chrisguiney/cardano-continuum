// Package kubetest is a fake kube.Client for command tests.
package kubetest

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/chrisguiney/cardano-continuum/api/v1alpha1"
	"github.com/chrisguiney/cardano-continuum/internal/imageset"
)

// Fake is a kube.Client holding one network and its pools; it records the
// patches applied to it.
type Fake struct {
	Net     *v1alpha1.ContinuumNetwork
	PoolSet []v1alpha1.ContinuumPool
	Applied []imageset.Change
}

// Network returns the fake's network whatever the name.
func (f *Fake) Network(context.Context, string) (*v1alpha1.ContinuumNetwork, error) {
	return f.Net, nil
}

// Pools returns the fake's pools whatever the network.
func (f *Fake) Pools(context.Context, string) ([]v1alpha1.ContinuumPool, error) {
	return f.PoolSet, nil
}

// Apply records c.
func (f *Fake) Apply(_ context.Context, c imageset.Change) error {
	f.Applied = append(f.Applied, c)
	return nil
}

// New returns a network "demo" with five dingo forging pools p0..p4 whose
// poolIds are pool0..pool4, listed out of order.
func New() *Fake {
	f := &Fake{Net: &v1alpha1.ContinuumNetwork{
		ObjectMeta: metav1.ObjectMeta{Name: "demo"},
		Spec:       v1alpha1.ContinuumNetworkSpec{Images: v1alpha1.ImageSet{v1alpha1.Dingo: "dingo:main"}},
	}}
	for _, i := range []int{4, 1, 3, 0, 2} {
		f.PoolSet = append(f.PoolSet, v1alpha1.ContinuumPool{
			ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("p%d", i)},
			Spec: v1alpha1.ContinuumPoolSpec{
				PoolID: fmt.Sprintf("pool%d", i), Implementation: v1alpha1.Dingo, Tier: v1alpha1.TierForging,
			},
		})
	}
	return f
}
