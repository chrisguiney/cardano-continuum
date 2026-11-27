//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/chrisguiney/cardano-continuum/api/v1alpha1"
	"github.com/chrisguiney/cardano-continuum/internal/genesis"
	"github.com/chrisguiney/cardano-continuum/internal/operator/render"
)

// TestGenesisBoots installs the operator, the genesis Job and a network of
// three cardano-node pools with the chart, and requires the pools to share
// one chain that reaches epoch 1 with every pool forging, and each pool
// reporting a ready workload.
func TestGenesisBoots(t *testing.T) {
	if testing.Short() {
		t.Skip("takes about 15 minutes")
	}
	spec := genesis.Default()
	b := bootNetwork(t, spec, 0)
	epochs := time.Duration(spec.EpochLength) * spec.SlotLength
	poll(t, time.Until(b.start.Add(epochs+4*time.Minute)), 15*time.Second, "all pools past epoch 1", func(ctx context.Context) (bool, error) {
		tips := tips(ctx, b.ns, b.pods, spec.Magic)
		t.Logf("tips: %+v", tips)
		if len(tips) != len(b.pods) {
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
	for _, pod := range b.pods {
		forged += checkForged(t, b.ns, pod, spec, 1)
	}
	checkOneChain(t, tips(t.Context(), b.ns, b.pods, spec.Magic), forged, spec)
	checkPoolPhases(t, b.ns, spec.Pools)
}

// booted is a network bootNetwork brought up.
type booted struct {
	*release
	// pods are the pools' pod names, genesis pool i's at index i.
	pods []string
	// start is the genesis systemStart.
	start time.Time
}

// bootNetwork installs the chart's operator, genesis Job and network in a
// fresh namespace, with spec.Pools genesis pools of which the last dingo are
// dingo pools, and stops the test unless the cardano-node pools share one
// chain (requireOneChain). sets go to the chart as well; the engine is off.
func bootNetwork(t *testing.T, spec genesis.Spec, dingo int, sets ...string) booted {
	t.Helper()
	ns := newNS(t)
	all := append([]string{
		startDelay(90 * time.Second),
		fmt.Sprintf("pools.cardanoNode=%d", spec.Pools-dingo),
		fmt.Sprintf("pools.dingo=%d", dingo),
	}, sets...)
	r := installChart(t, ns, parts{operator: true, genesis: true, network: true}, all...)
	waitGenesis(t, ns)

	var pods, nodes []string
	for i := range spec.Pools {
		p := poolObject(i)
		pod := render.PodName(render.Name(&p))
		poll(t, 5*time.Minute, 5*time.Second, pod+" to exist", func(ctx context.Context) (bool, error) {
			_, err := kubectl(ctx, "", "-n", ns, "get", "pod", pod)
			return err == nil, nil
		})
		kube(t, "", "-n", ns, "wait", "--for=condition=Ready", "--timeout=5m", "pod/"+pod)
		pods = append(pods, pod)
		if i < spec.Pools-dingo {
			nodes = append(nodes, pod)
		}
	}

	start := systemStart(t, publishedShelley(t, ns))
	requireOneChain(t, ns, nodes, spec, start)
	return booted{release: r, pods: pods, start: start}
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

// poolID is the PoolID of genesis pool i.
func poolID(i int) string { return fmt.Sprintf("continuum-pool-%d", i) }

// poolObject is genesis pool i as a forging cardano-node ContinuumPool, as
// the chart names it. A forger's workload name depends only on its PoolID
// (render.Name), so it names dingo pools' pods too.
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
// phase, whether the pool has caught up or not (phase.Judge).
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
