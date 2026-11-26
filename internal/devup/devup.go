// Package devup holds the pure parts of `make up`: the memory preflight and
// the manifests for a network and its pools. hack/up.sh does the I/O.
package devup

import (
	"encoding/json"
	"errors"
	"fmt"

	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/chrisguiney/cardano-continuum/api/v1alpha1"
	"github.com/chrisguiney/cardano-continuum/internal/operator"
)

// MaxPools is the largest pool count `make up` accepts: the default budget's
// pool count, which docs/single-machine.md sizes for a 48 GiB machine.
const MaxPools = v1alpha1.DefaultMaxPools

// CheckMemory reports whether the node's allocatable memory, a Kubernetes
// quantity, is at least minGiB gibibytes.
func CheckMemory(alloc string, minGiB int64) (bool, error) {
	q, err := resource.ParseQuantity(alloc)
	if err != nil {
		return false, fmt.Errorf("allocatable memory %q: %w", alloc, err)
	}
	return q.Value() >= minGiB<<30, nil
}

// Plan is the pools `make up` runs. cardano-node pools take the first genesis
// pool indices because the engine dials pool 0's node-to-client bridge, which
// dingo pools do not have.
type Plan struct {
	Nodes, Dingo               int
	Magic                      uint32
	EpochLength, SecurityParam int64
}

// Check reports the first reason p cannot run.
func (p Plan) Check() error {
	switch {
	case p.Nodes < 0 || p.Dingo < 0:
		return errors.New("pool counts must not be negative")
	case p.Nodes < 1:
		return errors.New("need at least one cardano-node pool")
	case p.Nodes+p.Dingo > MaxPools:
		return fmt.Errorf("at most %d pools fit a 48 GiB machine, got %d", MaxPools, p.Nodes+p.Dingo)
	}
	return nil
}

type list struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Items      []any  `json:"items"`
}

// Render returns a v1 List of the ContinuumNetwork named devnet and one
// forging ContinuumPool per genesis pool, under the default budget.
func Render(p Plan) (string, error) {
	if err := p.Check(); err != nil {
		return "", err
	}
	total := p.Nodes + p.Dingo
	items := []any{v1alpha1.ContinuumNetwork{
		TypeMeta:   metav1.TypeMeta{APIVersion: v1alpha1.GroupVersion.String(), Kind: "ContinuumNetwork"},
		ObjectMeta: metav1.ObjectMeta{Name: "devnet"},
		Spec: v1alpha1.ContinuumNetworkSpec{
			Magic: p.Magic, EpochLength: p.EpochLength, SecurityParam: p.SecurityParam,
			GenesisRef: "devnet-genesis",
			Topology:   v1alpha1.TopologyPolicy{Policy: v1alpha1.TopologyMesh},
			Budget: v1alpha1.BudgetSpec{
				MaxPools: v1alpha1.DefaultMaxPools, CPUMillis: v1alpha1.DefaultCPUMillis, MemoryMiB: v1alpha1.DefaultMemoryMiB,
			},
		},
	}}
	for i := range total {
		impl := v1alpha1.CardanoNode
		if i >= p.Nodes {
			impl = v1alpha1.Dingo
		}
		items = append(items, v1alpha1.ContinuumPool{
			TypeMeta:   metav1.TypeMeta{APIVersion: v1alpha1.GroupVersion.String(), Kind: "ContinuumPool"},
			ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("p%d", i)},
			Spec: v1alpha1.ContinuumPoolSpec{
				NetworkRef: "devnet", PoolID: fmt.Sprintf("continuum-pool-%d", i), Implementation: impl,
				Tier: v1alpha1.TierForging, KeysSecret: fmt.Sprintf("keys-%d", i),
			},
		})
	}
	raw, err := json.Marshal(list{APIVersion: "v1", Kind: "List", Items: items})
	if err != nil {
		return "", fmt.Errorf("render network: %w", err)
	}
	return string(raw), nil
}

// EngineNode is the host:port of pool 0's node-to-client bridge in namespace
// ns, which the engine follows.
func EngineNode(ns string) string {
	p := v1alpha1.ContinuumPool{
		ObjectMeta: metav1.ObjectMeta{Name: "p0"},
		Spec:       v1alpha1.ContinuumPoolSpec{PoolID: "continuum-pool-0", Implementation: v1alpha1.CardanoNode, Tier: v1alpha1.TierForging},
	}
	return fmt.Sprintf("%s.%s.svc.cluster.local:%d", operator.Name(&p), ns, operator.NtCPort)
}
