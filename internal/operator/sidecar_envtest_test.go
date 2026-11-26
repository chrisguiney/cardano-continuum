//go:build envtest

package operator

import (
	"slices"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"

	"github.com/chrisguiney/cardano-continuum/api/v1alpha1"
	"github.com/chrisguiney/cardano-continuum/internal/operator/render"
)

// editNetwork retries f on a fresh copy of the network until the update lands.
func (h *harness) editNetwork(f func(*v1alpha1.ContinuumNetwork)) {
	h.t.Helper()
	h.eventually("update network", func() bool {
		var n v1alpha1.ContinuumNetwork
		h.must(h.c.Get(h.ctx, types.NamespacedName{Namespace: h.ns, Name: "net"}, &n))
		f(&n)
		return h.c.Update(h.ctx, &n) == nil
	})
}

// TestParquetSourceMoveRollsTwoPods: enabling the sidecar rolls only the
// default source (the lowest relay); moving the source to the forger rolls
// exactly the old and new source, the forger through the fence. The third
// pod is never touched and the choice is recorded in status.
func TestParquetSourceMoveRollsTwoPods(t *testing.T) {
	h := newHarness(t, nil)
	h.secret("f-keys", "opcert")
	h.pool("f", v1alpha1.CardanoNode, v1alpha1.TierForging, nil)
	for _, r := range []string{"r1", "r2"} {
		h.pool(r, v1alpha1.CardanoNode, v1alpha1.TierValidating, func(p *v1alpha1.ContinuumPool) { p.Spec.PoolID = "pool-" + r })
	}
	h.startOperator()
	names := map[string]string{}
	uids := map[string]types.UID{}
	for _, n := range []string{"f", "r1", "r2"} {
		names[n] = render.PodName(render.Name(h.get(n)))
		uids[n] = h.running(names[n], v1alpha1.CardanoNode).UID
	}
	// rolled waits for pool n's pod to be a new one with or without the
	// sidecar, then records it.
	rolled := func(n string, sidecar bool) {
		h.t.Helper()
		h.eventually(n+" rolled", func() bool {
			p, ok := h.pod(names[n])
			if !ok || p.UID == uids[n] || p.DeletionTimestamp != nil || p.Status.Phase != corev1.PodRunning {
				return false
			}
			if _, has := sidecarOf(p.Spec); has != sidecar {
				return false
			}
			uids[n] = p.UID
			return true
		})
	}
	same := func(n string) {
		h.t.Helper()
		h.consistently(n+" untouched", time.Second, func() bool {
			p, ok := h.pod(names[n])
			return ok && p.UID == uids[n]
		})
	}
	source := func() string {
		var n v1alpha1.ContinuumNetwork
		h.must(h.c.Get(h.ctx, types.NamespacedName{Namespace: h.ns, Name: "net"}, &n))
		return n.Status.ParquetPool
	}

	h.editNetwork(func(n *v1alpha1.ContinuumNetwork) {
		n.Spec.Parquet = v1alpha1.ParquetSpec{Enabled: true, Image: "mithril2pq:test", IntervalSeconds: 60, OutputClaim: "parquet"}
	})
	rolled("r1", true)
	same("f")
	same("r2")
	h.eventually("status names r1", func() bool { return source() == "r1" })

	var mu sync.Mutex
	var events []event
	h.record(&events, &mu)
	oldF := uids["f"]
	h.editNetwork(func(n *v1alpha1.ContinuumNetwork) { n.Spec.Parquet.Pool = "pool1abc" })
	rolled("r1", false)
	rolled("f", true)
	same("r2")
	h.eventually("status names f", func() bool { return source() == "f" })

	mu.Lock()
	defer mu.Unlock()
	var gone, created uint64
	for _, e := range events {
		if e.kind != "Pod" {
			continue
		}
		if e.uid == oldF && e.typ == watch.Deleted {
			gone = e.rv
		}
		if e.uid == uids["f"] && e.typ == watch.Added {
			created = e.rv
		}
	}
	if gone == 0 || created == 0 || created < gone {
		t.Fatalf("old forger gone at revision %d, new one created at %d", gone, created)
	}
	if got := h.keyedPods(render.RoleForger); got != 1 {
		t.Errorf("%d forger pods, want 1", got)
	}
}

// sidecarOf returns the mithril2pq container of the pod, or false.
func sidecarOf(spec corev1.PodSpec) (corev1.Container, bool) {
	i := slices.IndexFunc(spec.Containers, func(c corev1.Container) bool { return c.Name == "mithril2pq" })
	if i < 0 {
		return corev1.Container{}, false
	}
	return spec.Containers[i], true
}
