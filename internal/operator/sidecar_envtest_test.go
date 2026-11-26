//go:build envtest

package operator

import (
	"sync"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"

	"github.com/chrisguiney/cardano-continuum/api/v1alpha1"
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

// TestParquetToggleRollsThroughFence: enabling the sidecar on the network
// changes the pod template of a forging pool. The StatefulSet is updated in
// place, and the old pod is gone before its replacement is created, so no
// second pod ever runs beside the forger.
func TestParquetToggleRollsThroughFence(t *testing.T) {
	h := newHarness(t, nil)
	h.secret("a-keys", "opcert")
	h.pool("a", v1alpha1.CardanoNode, v1alpha1.TierForging, nil)
	h.startOperator()
	set := Name(h.get("a"))
	name := PodName(set)
	old := h.running(name, v1alpha1.CardanoNode)
	before, ok := h.set(set)
	if !ok {
		t.Fatal("no StatefulSet before the toggle")
	}

	var mu sync.Mutex
	var events []event
	h.record(&events, &mu)
	h.kube.Withhold(name, true)
	h.editNetwork(func(n *v1alpha1.ContinuumNetwork) {
		n.Spec.Parquet = v1alpha1.ParquetSpec{Enabled: true, Image: "mithril2pq:test", IntervalSeconds: 60, OutputClaim: "parquet"}
	})
	h.eventually("old pod terminating", func() bool {
		p, ok := h.pod(name)
		return ok && p.DeletionTimestamp != nil
	})
	h.consistently("old pod object held, no replacement", 1500*time.Millisecond, func() bool {
		p, ok := h.pod(name)
		return ok && p.UID == old.UID && h.keyedPods(RoleForger) == 1
	})
	h.kube.Withhold(name, false)
	next := h.runningImage(name, old.Spec.Containers[0].Image)

	after, ok := h.set(set)
	if !ok || after.UID != before.UID {
		t.Fatalf("StatefulSet replaced by the toggle: %v", after)
	}
	if _, ok := sidecarOf(next.Spec); !ok {
		t.Errorf("new pod has no mithril2pq container: %v", next.Spec.Containers)
	}

	mu.Lock()
	defer mu.Unlock()
	var gone, created uint64
	for _, e := range events {
		if e.kind != "Pod" {
			continue
		}
		if e.uid == old.UID && e.typ == watch.Deleted {
			gone = e.rv
		}
		if e.uid == next.UID && e.typ == watch.Added {
			created = e.rv
		}
	}
	if gone == 0 || created == 0 || created < gone {
		t.Fatalf("old pod gone at revision %d, new pod created at %d", gone, created)
	}
}
