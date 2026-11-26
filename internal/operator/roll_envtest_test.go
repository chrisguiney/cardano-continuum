//go:build envtest

package operator

// The image-roll tests listed in models/specs/pool-forging/README.md, each
// named for the config or contract rule it comes from.

import (
	"context"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"

	"github.com/chrisguiney/cardano-continuum/api/v1alpha1"
	"github.com/chrisguiney/cardano-continuum/internal/operator/fakekube"
	"github.com/chrisguiney/cardano-continuum/internal/operator/render"
)

const (
	imageV1 = "registry.test/node:v1"
	imageV2 = "registry.test/node:v2"
)

// pinned is the image a pod runs for ref under fakeResolver.
func pinned(h *harness, ref string) string {
	img, _ := fakeResolver{}.Resolve(h.ctx, ref)
	return pin(ref, img.Digest)
}

func (h *harness) setImage(pool, ref string) {
	h.t.Helper()
	h.edit(pool, func(p *v1alpha1.ContinuumPool) { p.Spec.Image = ref })
}

// runningImage waits until pod name runs image and returns it.
func (h *harness) runningImage(name, image string) *corev1.Pod {
	h.t.Helper()
	var pod *corev1.Pod
	h.eventually(name+" running "+image, func() bool {
		p, ok := h.pod(name)
		pod = p
		return ok && p.DeletionTimestamp == nil && p.Status.Phase == corev1.PodRunning && p.Spec.Containers[0].Image == image
	})
	return pod
}

// rolling starts a forging pool on imageV1 and returns its harness, the
// pod name and the running pod.
func rolling(t *testing.T) (*harness, string, *corev1.Pod) {
	h := newHarness(t, nil)
	h.secret("a-keys", "opcert")
	h.pool("a", v1alpha1.CardanoNode, v1alpha1.TierForging, func(p *v1alpha1.ContinuumPool) { p.Spec.Image = imageV1 })
	h.startOperator()
	name := render.PodName(render.Name(h.get("a")))
	return h, name, h.runningImage(name, pinned(h, imageV1))
}

// TestRollIsInPlace: ForgingRollSurgeBug. A new digest changes the image of
// the same StatefulSet; its name and selector do not change.
func TestRollIsInPlace(t *testing.T) {
	h, name, old := rolling(t)
	before, ok := h.set(render.Name(h.get("a")))
	if !ok {
		t.Fatal("no StatefulSet before the roll")
	}
	h.setImage("a", imageV2)
	h.runningImage(name, pinned(h, imageV2))
	after, ok := h.set(before.Name)
	if !ok || after.UID != before.UID {
		t.Fatalf("StatefulSet replaced: %v", after)
	}
	if after.Name != before.Name || !selectorsEqual(after.Spec.Selector, before.Spec.Selector) {
		t.Errorf("name or selector changed: %s %v", after.Name, after.Spec.Selector)
	}
	if after.Spec.Template.Spec.Containers[0].Image != pinned(h, imageV2) {
		t.Errorf("template image %s", after.Spec.Template.Spec.Containers[0].Image)
	}
	if p, _ := h.pod(old.Name); p.UID == old.UID {
		t.Error("the old pod was kept")
	}
	if n := h.keyedPods(render.RoleForger); n != 1 {
		t.Errorf("%d keyed pods after the roll", n)
	}
}

func selectorsEqual(a, b *metav1.LabelSelector) bool {
	if a == nil || b == nil || len(a.MatchLabels) != len(b.MatchLabels) {
		return a == b
	}
	for k, v := range a.MatchLabels {
		if b.MatchLabels[k] != v {
			return false
		}
	}
	return true
}

// TestRollWaitsForPodGone: ForgingRollSurgeBug. With termination held open
// the new-digest pod is created only after the old pod object is gone.
func TestRollWaitsForPodGone(t *testing.T) {
	h, name, old := rolling(t)
	var mu sync.Mutex
	var events []event
	h.record(&events, &mu)
	h.kube.Withhold(name, true)
	h.setImage("a", imageV2)
	h.eventually("old pod terminating", func() bool {
		p, ok := h.pod(name)
		return ok && p.DeletionTimestamp != nil
	})
	h.consistently("old pod object held, no replacement", 1500*time.Millisecond, func() bool {
		p, ok := h.pod(name)
		return ok && p.UID == old.UID && h.keyedPods(render.RoleForger) == 1
	})
	h.kube.Withhold(name, false)
	next := h.runningImage(name, pinned(h, imageV2))

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

// TestRollOnLostNodeHolds: ForgingLostIsGoneBug. A roll whose old pod sits
// on a NotReady node does not proceed and the pool reports the fence.
func TestRollOnLostNodeHolds(t *testing.T) {
	h, name, old := rolling(t)
	h.kube.Lose()
	h.must(fakekube.SetReady(h.ctx, h.c, h.node, false))
	h.setImage("a", imageV2)
	h.eventually("fence condition naming pod and node", func() bool {
		return h.hasCondition("a", CondFence, metav1.ConditionTrue, ReasonNodeUnreachable, old.Name, h.node)
	})
	h.consistently("old pod held, no replacement", time.Second, func() bool {
		p, ok := h.pod(name)
		return ok && p.UID == old.UID && h.keyedPods(render.RoleForger) == 1
	})
}

// TestRollKeepsSecret: rule 9. The Secret is untouched and the new pod
// mounts the same opcert.
func TestRollKeepsSecret(t *testing.T) {
	h, name, old := rolling(t)
	key := types.NamespacedName{Namespace: h.ns, Name: "a-keys"}
	var before corev1.Secret
	h.must(h.c.Get(h.ctx, key, &before))
	h.setImage("a", imageV2)
	next := h.runningImage(name, pinned(h, imageV2))
	var after corev1.Secret
	h.must(h.c.Get(h.ctx, key, &after))
	if after.ResourceVersion != before.ResourceVersion {
		t.Errorf("Secret resourceVersion %s, was %s", after.ResourceVersion, before.ResourceVersion)
	}
	if !hasVolumeFrom(old.Spec, "a-keys") || !hasVolumeFrom(next.Spec, "a-keys") {
		t.Error("a pod does not mount a-keys")
	}
	if string(after.Data[render.OpCert]) != "opcert" {
		t.Errorf("opcert %q", after.Data[render.OpCert])
	}
}

// TestRollRecordsDigests: the pool's status names the digest it left, the
// digest it runs and when it rolled; a first resolution is not a roll.
func TestRollRecordsDigests(t *testing.T) {
	h, name, _ := rolling(t)
	if r := h.get("a").Status.LastRoll; r != nil {
		t.Fatalf("a first start recorded a roll: %+v", r)
	}
	oldDigest := h.get("a").Status.Image.Digest
	start := time.Now().Add(-time.Second)
	h.setImage("a", imageV2)
	h.runningImage(name, pinned(h, imageV2))
	h.eventually("roll recorded", func() bool {
		r := h.get("a").Status.LastRoll
		return r != nil && r.From == oldDigest && r.To == h.get("a").Status.Image.Digest && r.To != r.From && r.Time.After(start)
	})
}

// TestRollDuringScenario: DuplicateScoped. Rolling either pod of a scenario
// pair, or both at once, never gives a third keyed pod, and no pod name has
// two objects alive.
func TestRollDuringScenario(t *testing.T) {
	h := newHarness(t, nil)
	h.secret("a-keys", "opcert")
	h.secret("d-keys", "opcert")
	h.pool("a", v1alpha1.CardanoNode, v1alpha1.TierForging, func(p *v1alpha1.ContinuumPool) { p.Spec.Image = imageV1 })
	h.pool("d", v1alpha1.CardanoNode, v1alpha1.TierForging, func(p *v1alpha1.ContinuumPool) {
		p.Spec.UnsafeDuplicateOf, p.Spec.Image = "a", imageV1
	})
	h.startOperator()
	a, d := render.PodName(render.Name(h.get("a"))), render.PodName(render.Name(h.get("d")))
	h.runningImage(a, pinned(h, imageV1))
	h.runningImage(d, pinned(h, imageV1))

	var mu sync.Mutex
	var forgers, dups int
	h.run("monitor", func(ctx context.Context) error {
		for ctx.Err() == nil {
			f, du := h.keyedPods(render.RoleForger), h.keyedPods(render.RoleDuplicate)
			mu.Lock()
			forgers, dups = max(forgers, f), max(dups, du)
			mu.Unlock()
			h.pause()
		}
		return nil
	})

	h.setImage("a", imageV2)
	h.runningImage(a, pinned(h, imageV2))
	h.setImage("d", imageV2)
	h.runningImage(d, pinned(h, imageV2))
	h.setImage("a", imageV1)
	h.setImage("d", imageV1)
	h.runningImage(a, pinned(h, imageV1))
	h.runningImage(d, pinned(h, imageV1))

	mu.Lock()
	defer mu.Unlock()
	if forgers > 1 || dups > 1 {
		t.Fatalf("at most %d forgers and %d duplicates at once", forgers, dups)
	}
}
