// Package devup holds the pure parts of `make up`: the memory preflight,
// the manifests for a network and its pools, and the attacks ATTACKS
// selects. hack/up.sh does the I/O.
package devup

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/chrisguiney/cardano-continuum/api/v1alpha1"
	"github.com/chrisguiney/cardano-continuum/internal/adversary"
	"github.com/chrisguiney/cardano-continuum/internal/operator/render"
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
	return fmt.Sprintf("%s.%s.svc.cluster.local:%d", render.Name(&p), ns, render.NtCPort)
}

// AttackPlan is what `make up` passes for its ATTACKS: the engine's
// --attack value, empty for none, and the continuum-genesis arguments the
// attacks need.
type AttackPlan struct {
	Engine  string
	Genesis []string
}

// Attacks parses ATTACKS: a comma-separated list of tier-1 attacks, "none",
// or empty for every attack a stock genesis supports. An attack that needs
// a genesis override (adversary.GenesisOverrides) is opt-in, and naming it
// adds the overrides and the flag that allows them.
func Attacks(s string) (AttackPlan, error) {
	var ns []adversary.Name
	switch s = strings.TrimSpace(s); s {
	case "none":
		return AttackPlan{}, nil
	case "":
		for _, n := range adversary.Names() {
			if adversary.GenesisOverrides(n) == nil {
				ns = append(ns, n)
			}
		}
	default:
		for _, part := range strings.Split(s, ",") {
			n, err := adversary.Parse(strings.TrimSpace(part))
			if err != nil {
				return AttackPlan{}, fmt.Errorf("ATTACKS: %w", err)
			}
			if !slices.Contains(ns, n) {
				ns = append(ns, n)
			}
		}
	}
	var p AttackPlan
	names := make([]string, len(ns))
	for i, n := range ns {
		names[i] = string(n)
		for _, o := range adversary.GenesisOverrides(n) {
			p.Genesis = append(p.Genesis, "-override="+o)
		}
	}
	if len(p.Genesis) > 0 {
		p.Genesis = append(p.Genesis, "-allow-inconsistent-exunits")
	}
	p.Engine = strings.Join(names, ",")
	return p, nil
}

// RunIdentity is what an engine's event log header binds a run to, as far
// as `make up` can change it without a new genesis: the engine image, the
// profile set, the seed and the attacks (which add adversary profiles).
// An engine pod resuming a log under another identity refuses it.
type RunIdentity struct {
	Image, Profiles, Seed, Attacks string
}

// String encodes the identity for the engine StatefulSet's annotation. The
// seed is a secret, so only its SHA-256 is written.
func (r RunIdentity) String() string {
	h := sha256.Sum256([]byte(r.Seed))
	return strings.Join([]string{"image=" + r.Image, "profiles=" + r.Profiles,
		"seed=" + hex.EncodeToString(h[:8]), "attacks=" + r.Attacks}, ";")
}

// runFields names each identity field in the order RunChange reports it,
// with the line up.sh logs.
var runFields = []struct{ key, why string }{
	{"image", "engine binary changed: a run can't be resumed by a different binary"},
	{"seed", "seed changed: a run can't be resumed under another seed"},
	{"profiles", "profiles changed: a run can't be resumed under another profile set"},
	{"attacks", "attacks changed: a run can't be resumed with other adversaries"},
}

// RunChange returns why a deployed engine, whose annotation is deployed,
// cannot resume under want, or "" when it can.
func RunChange(deployed string, want RunIdentity) string {
	if deployed == "" {
		return "engine identity unrecorded: the deployed engine predates it, so a run can't be known to resume"
	}
	have := map[string]string{}
	for _, kv := range strings.Split(deployed, ";") {
		k, v, _ := strings.Cut(kv, "=")
		have[k] = v
	}
	next := map[string]string{}
	for _, kv := range strings.Split(want.String(), ";") {
		k, v, _ := strings.Cut(kv, "=")
		next[k] = v
	}
	for _, f := range runFields {
		if have[f.key] != next[f.key] {
			return f.why
		}
	}
	return ""
}
