//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/chrisguiney/cardano-continuum/api/v1alpha1"
	"github.com/chrisguiney/cardano-continuum/internal/genesis"
	"github.com/chrisguiney/cardano-continuum/internal/operator"
)

const operatorBinding = "continuum-e2e-operator"

// TestGenesisBoots deploys the operator, applies a ContinuumNetwork and three
// cardano-node ContinuumPools over the Job-rendered genesis, and requires the
// pools to share one chain that reaches epoch 1 with every pool forging, and each pool
// reporting a ready workload.
func TestGenesisBoots(t *testing.T) {
	if testing.Short() {
		t.Skip("takes about 15 minutes")
	}
	spec := genesis.Default()
	ns, pods, start := bootNetwork(t, spec)
	epochs := time.Duration(spec.EpochLength) * spec.SlotLength
	poll(t, time.Until(start.Add(epochs+4*time.Minute)), 15*time.Second, "all pools past epoch 1", func(ctx context.Context) (bool, error) {
		tips := tips(ctx, ns, pods, spec.Magic)
		t.Logf("tips: %+v", tips)
		if len(tips) != len(pods) {
			return false, nil
		}
		for _, tp := range tips {
			if tp.Epoch < 1 {
				return false, nil
			}
		}
		return true, nil
	})
	forged := 0
	for _, pod := range pods {
		forged += checkForged(t, ns, pod, spec, 1)
	}
	checkOneChain(t, tips(t.Context(), ns, pods, spec.Magic), forged, spec)
	checkPoolPhases(t, ns, spec.Pools)
}

// bootNetwork runs the operator in a fresh namespace, renders the genesis
// with the Job and brings up one forging cardano-node ContinuumPool per
// genesis pool, and stops the test unless they share one chain
// (requireOneChain). It returns the namespace, the pools' pod names (pool i's
// first) and the system start.
func bootNetwork(t *testing.T, spec genesis.Spec) (string, []string, time.Time) {
	t.Helper()
	return bootNetworkWith(t, spec, poolObject)
}

// bootNetworkWith boots the network with pool(i) as genesis pool i's
// ContinuumPool.
func bootNetworkWith(t *testing.T, spec genesis.Spec, pool func(int) v1alpha1.ContinuumPool) (string, []string, time.Time) {
	t.Helper()
	ns := newNS(t)
	installOperator(t, ns)
	// Keyed pods carry the operator's fence finalizer, so the pools must go
	// while the operator still runs; this cleanup runs before newNS's.
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 3*time.Minute)
		defer cancel()
		_, _ = kubectl(ctx, "", "-n", ns, "delete", "continuumpools", "--all", "--wait=true", "--timeout=60s")
		// Deleting a pool does not wait for its pod, whose finalizer only the
		// operator removes.
		_, _ = kubectl(ctx, "", "-n", ns, "wait", "--for=delete", "pod", "-l", operator.LabelWorkload, "--timeout=100s")
	})
	prepullNodeImage(t, ns)
	kube(t, genesisResources, "-n", ns, "apply", "-f", "-")
	var args []string
	if spec.Pools != genesis.Default().Pools {
		args = append(args, fmt.Sprintf("-pools=%d", spec.Pools))
	}
	deployGenesis(t, ns, 90*time.Second, args...)
	read := genesisReader(t, ns)

	publishGenesis(t, ns, read, spec)
	pods := applyNetworkWith(t, ns, spec, pool)
	var nodes []string
	for i, pod := range pods {
		poll(t, 5*time.Minute, 5*time.Second, pod+" to exist", func(ctx context.Context) (bool, error) {
			_, err := kubectl(ctx, "", "-n", ns, "get", "pod", pod)
			return err == nil, nil
		})
		kube(t, "", "-n", ns, "wait", "--for=condition=Ready", "--timeout=5m", "pod/"+pod)
		if p := pool(i); p.Spec.Implementation == v1alpha1.CardanoNode {
			nodes = append(nodes, pod)
		}
	}
	start := systemStart(t, read("config/"+genesis.ShelleyFile))
	requireOneChain(t, ns, nodes, spec, start)
	return ns, pods, start
}

// installOperator applies the CRDs and RBAC and runs the operator in ns.
func installOperator(t *testing.T, ns string, args ...string) {
	t.Helper()
	root := repoRoot(t)
	crds := filepath.Join(root, "config", "crd", "bases")
	role := filepath.Join(root, "config", "rbac", "role.yaml")
	kube(t, "", "apply", "-f", crds)
	kube(t, "", "apply", "-f", role)
	t.Cleanup(func() {
		ctx := context.WithoutCancel(t.Context())
		_, _ = kubectl(ctx, "", "delete", "clusterrolebinding", operatorBinding, "--ignore-not-found")
		_, _ = kubectl(ctx, "", "delete", "-f", role, "--ignore-not-found")
		_, _ = kubectl(ctx, "", "delete", "-f", crds, "--ignore-not-found")
	})
	for _, crd := range []string{"continuumnetworks", "continuumpools"} {
		kube(t, "", "wait", "--for=condition=Established", "--timeout=60s", "crd/"+crd+".continuum.blinklabs.io")
	}
	kube(t, "", "-n", ns, "create", "serviceaccount", "continuum-operator")
	kube(t, "", "delete", "clusterrolebinding", operatorBinding, "--ignore-not-found")
	kube(t, "", "create", "clusterrolebinding", operatorBinding, "--clusterrole=continuum-operator",
		"--serviceaccount="+ns+":continuum-operator")
	kube(t, operatorDeployment(koBuild(t, "continuum-operator"), args), "-n", ns, "apply", "-f", "-")
	kube(t, "", "-n", ns, "wait", "--for=condition=Available", "--timeout=3m", "deployment/continuum-operator")
}

func operatorDeployment(image string, extra []string) string {
	args := append([]string{"--leader-elect=false"}, extra...)
	return `apiVersion: apps/v1
kind: Deployment
metadata:
  name: continuum-operator
spec:
  replicas: 1
  selector:
    matchLabels: {app: continuum-operator}
  template:
    metadata:
      labels: {app: continuum-operator}
    spec:
      serviceAccountName: continuum-operator
      containers:
        - name: operator
          image: ` + image + `
          args: [` + strings.Join(args, ", ") + `]
`
}

// prepullNodeImage pulls the operator's cardano-node image on every worker
// before the genesis Job fixes systemStart: a node that starts more than the
// stability window (3k/f slots) after slot 0 has no ledger view to forge in
// and never forges.
func prepullNodeImage(t *testing.T, ns string) {
	t.Helper()
	image := operator.CardanoNode{}.Image()
	out := kube(t, "", "get", "nodes", "-l", "!node-role.kubernetes.io/control-plane", "-o", "name")
	var pods []string
	for i, node := range strings.Fields(out) {
		name := fmt.Sprintf("prepull-%d", i)
		pod := corev1.Pod{
			TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"},
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec: corev1.PodSpec{
				NodeName:                      strings.TrimPrefix(node, "node/"),
				TerminationGracePeriodSeconds: new(int64),
				Containers: []corev1.Container{{
					Name: "main", Image: image, Command: []string{"sleep", "3600"},
				}},
			},
		}
		apply(t, ns, pod)
		pods = append(pods, "pod/"+name)
	}
	kube(t, "", append([]string{"-n", ns, "wait", "--for=condition=Ready", "--timeout=8m"}, pods...)...)
	kube(t, "", append([]string{"-n", ns, "delete", "--wait=false"}, pods...)...)
}

// apply creates or updates obj in ns.
func apply(t *testing.T, ns string, obj any) {
	t.Helper()
	raw, err := json.Marshal(obj)
	if err != nil {
		t.Fatal(err)
	}
	kube(t, string(raw), "-n", ns, "apply", "-f", "-")
}

// keyFiles are the files of a pool's keys Secret, as the operator names them.
var keyFiles = []string{operator.KESKey, operator.VRFKey, operator.OpCert}

// publishGenesis copies the rendered config into the ConfigMap the network's
// genesisRef names, and each pool's keys into a Secret. The ConfigMap holds
// the files at its top level, as the operator mounts it.
func publishGenesis(t *testing.T, ns string, read func(string) string, s genesis.Spec) {
	t.Helper()
	cm := corev1.ConfigMap{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"},
		ObjectMeta: metav1.ObjectMeta{Name: "devnet-genesis"},
		Data:       map[string]string{},
	}
	for _, f := range []string{genesis.ConfigFile, genesis.ByronFile, genesis.ShelleyFile, genesis.AlonzoFile, genesis.ConwayFile} {
		cm.Data[f] = read("config/" + f)
	}
	apply(t, ns, cm)
	for i := range s.Pools {
		sec := corev1.Secret{
			TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
			ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("keys-%d", i)},
			StringData: map[string]string{},
		}
		for _, f := range keyFiles {
			sec.StringData[f] = read(fmt.Sprintf("pools/%d/%s", i, f))
		}
		apply(t, ns, sec)
	}
}

// poolID is the PoolID of genesis pool i.
func poolID(i int) string { return fmt.Sprintf("continuum-pool-%d", i) }

// applyNetwork creates the ContinuumNetwork and one forging ContinuumPool per
// genesis pool, and returns the names of the pods the operator will run.
func applyNetwork(t *testing.T, ns string, s genesis.Spec) []string {
	t.Helper()
	return applyNetworkWith(t, ns, s, poolObject)
}

func applyNetworkWith(t *testing.T, ns string, s genesis.Spec, pool func(int) v1alpha1.ContinuumPool) []string {
	t.Helper()
	apply(t, ns, v1alpha1.ContinuumNetwork{
		TypeMeta:   metav1.TypeMeta{APIVersion: v1alpha1.GroupVersion.String(), Kind: "ContinuumNetwork"},
		ObjectMeta: metav1.ObjectMeta{Name: "devnet"},
		Spec: v1alpha1.ContinuumNetworkSpec{
			Magic: s.Magic, EpochLength: s.EpochLength, SecurityParam: s.SecurityParam,
			GenesisRef: "devnet-genesis",
			Topology:   v1alpha1.TopologyPolicy{Policy: v1alpha1.TopologyMesh},
			Budget:     v1alpha1.BudgetSpec{CPUMillis: 4000, MemoryMiB: 8192, MaxPools: int32(s.Pools) + 1},
		},
	})
	var pods []string
	for i := range s.Pools {
		p := pool(i)
		apply(t, ns, p)
		pods = append(pods, operator.PodName(operator.Name(&p)))
	}
	return pods
}

// poolObject is genesis pool i as a forging cardano-node ContinuumPool.
func poolObject(i int) v1alpha1.ContinuumPool {
	return v1alpha1.ContinuumPool{
		TypeMeta:   metav1.TypeMeta{APIVersion: v1alpha1.GroupVersion.String(), Kind: "ContinuumPool"},
		ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("p%d", i)},
		Spec: v1alpha1.ContinuumPoolSpec{
			NetworkRef: "devnet", PoolID: poolID(i), Implementation: v1alpha1.CardanoNode,
			Tier: v1alpha1.TierForging, KeysSecret: fmt.Sprintf("keys-%d", i),
		},
	}
}

// checkPoolPhases requires every pool's workload to be ready: a running
// phase, whether the pool has caught up or not (operator.Judge).
func checkPoolPhases(t *testing.T, ns string, pools int) {
	t.Helper()
	poll(t, 2*time.Minute, 5*time.Second, "pools to report a running phase", func(ctx context.Context) (bool, error) {
		for i := range pools {
			phase, err := kubectl(ctx, "", "-n", ns, "get", "continuumpool", fmt.Sprintf("p%d", i), "-o", "jsonpath={.status.phase}")
			if err != nil {
				return false, err
			}
			if !v1alpha1.PoolPhase(phase).Running() {
				return false, nil
			}
		}
		return true, nil
	})
}
