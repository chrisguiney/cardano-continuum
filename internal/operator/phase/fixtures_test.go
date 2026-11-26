package phase

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/chrisguiney/cardano-continuum/api/v1alpha1"
)

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
