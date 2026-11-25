//go:build envtest

package operator

// The node 21 tests listed in models/specs/pool-forging/README.md, each
// named for the config or contract rule it comes from.

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/chrisguiney/cardano-continuum/api/v1alpha1"
	"github.com/chrisguiney/cardano-continuum/internal/operator/fakekube"
)

// emptyCache is a client whose StatefulSet and pod reads find nothing, as
// an informer cache that has not synced, and which records every
// StatefulSet create's error. Writes reach the API server.
type emptyCache struct {
	client.Client

	mu sync.Mutex
	// protects creates
	creates []error
}

func (e *emptyCache) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if _, ok := obj.(*appsv1.StatefulSet); ok {
		return apierrors.NewNotFound(appsv1.Resource("statefulsets"), key.Name)
	}
	return e.Client.Get(ctx, key, obj, opts...)
}

func (e *emptyCache) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	switch list.(type) {
	case *appsv1.StatefulSetList, *corev1.PodList:
		return nil
	}
	return e.Client.List(ctx, list, opts...)
}

func (e *emptyCache) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	err := e.Client.Create(ctx, obj, opts...)
	if _, ok := obj.(*appsv1.StatefulSet); ok {
		e.mu.Lock()
		e.creates = append(e.creates, err)
		e.mu.Unlock()
	}
	return err
}

// TestCreateIsTheFence: ForgingCacheFenceBug. Nothing the reconciler reads
// says the keyed pod exists, and still no second one appears.
func TestCreateIsTheFence(t *testing.T) {
	h := newHarness(t, nil)
	h.secret("a-keys", "opcert")
	p := h.pool("a", v1alpha1.CardanoNode, v1alpha1.TierForging, nil)
	set := h.renderDirect(p)
	held := h.running(PodName(set.Name), v1alpha1.CardanoNode)

	c := &emptyCache{Client: h.c}
	res, err := NewPools(c, Drivers(), fakeResolver{}, quiet()).Reconcile(h.ctx, request(p))
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if res.RequeueAfter == 0 {
		t.Error("held fence did not requeue")
	}
	c.mu.Lock()
	creates := c.creates
	c.mu.Unlock()
	if len(creates) != 1 || !apierrors.IsAlreadyExists(creates[0]) {
		t.Fatalf("StatefulSet creates %v, want one AlreadyExists", creates)
	}
	if n := h.keyedPods(RoleForger); n != 1 {
		t.Fatalf("%d keyed pods, want 1", n)
	}
	if pod, _ := h.pod(held.Name); pod.UID != held.UID {
		t.Fatal("the held pod was replaced")
	}
}

// event is one watch event with its etcd revision. Revisions are opaque to
// clients in general, but on one etcd they are a single increasing counter
// across resources, which is what lets the test order a pod's deletion
// against a StatefulSet's creation.
type event struct {
	typ  watch.EventType
	kind string
	name string
	uid  types.UID
	impl string
	rv   uint64
}

func (h *harness) record(events *[]event, mu *sync.Mutex) {
	h.t.Helper()
	wc, err := client.NewWithWatch(suite.cfg, client.Options{Scheme: suite.scheme})
	h.must(err)
	for _, list := range []client.ObjectList{&corev1.PodList{}, &appsv1.StatefulSetList{}} {
		w, err := wc.Watch(h.ctx, list, client.InNamespace(h.ns))
		h.must(err)
		h.t.Cleanup(w.Stop)
		go func() {
			for e := range w.ResultChan() {
				obj, ok := e.Object.(client.Object)
				if !ok {
					continue
				}
				rv, _ := strconv.ParseUint(obj.GetResourceVersion(), 10, 64)
				kind := "Pod"
				if _, ok := obj.(*appsv1.StatefulSet); ok {
					kind = "StatefulSet"
				}
				mu.Lock()
				*events = append(*events, event{e.Type, kind, obj.GetName(), obj.GetUID(), obj.GetLabels()[LabelImpl], rv})
				mu.Unlock()
			}
		}()
	}
}

// TestSwitchImplSerial: ForgingCreateBeforeDeleteBug. The new driver's
// StatefulSet is created only after the old pod object is gone, in both
// directions between cardano-node and dingo.
func TestSwitchImplSerial(t *testing.T) {
	for _, tc := range []struct{ from, to v1alpha1.Implementation }{
		{v1alpha1.CardanoNode, v1alpha1.Dingo},
		{v1alpha1.Dingo, v1alpha1.CardanoNode},
	} {
		t.Run(string(tc.from)+"-to-"+string(tc.to), func(t *testing.T) {
			switchSerial(t, tc.from, tc.to)
		})
	}
}

func switchSerial(t *testing.T, from, to v1alpha1.Implementation) {
	h := newHarness(t, nil)
	h.secret("a-keys", "opcert")
	h.pool("a", from, v1alpha1.TierForging, nil)
	h.startOperator()
	name := Name(h.get("a"))
	old := h.running(PodName(name), from)

	var mu sync.Mutex
	var events []event
	h.record(&events, &mu)
	h.edit("a", func(p *v1alpha1.ContinuumPool) { p.Spec.Implementation = to })
	h.running(PodName(name), to)

	mu.Lock()
	defer mu.Unlock()
	var gone, created uint64
	for _, e := range events {
		if e.kind == "Pod" && e.uid == old.UID && e.typ == watch.Deleted {
			gone = e.rv
		}
		if e.kind == "StatefulSet" && e.impl == string(to) && e.typ == watch.Added && created == 0 {
			created = e.rv
		}
	}
	if gone == 0 || created == 0 {
		t.Fatalf("old pod deleted at %d, %s StatefulSet created at %d: missing event in %+v", gone, to, created, events)
	}
	if created < gone {
		t.Fatalf("%s StatefulSet created at revision %d, before the old pod was gone at %d", to, created, gone)
	}
	if n := h.keyedPods(RoleForger); n != 1 {
		t.Fatalf("%d keyed pods after the switch, want 1", n)
	}
	if got, ok := h.set(name); !ok || got.Labels[LabelImpl] != string(to) {
		t.Fatalf("StatefulSet %s after the switch: %v", name, got)
	}
}

// TestSwitchWaitsForStuckTermination: ForgingProbeZombieBug. A pod whose
// termination is never reported keeps the new driver out.
func TestSwitchWaitsForStuckTermination(t *testing.T) {
	h := newHarness(t, nil)
	h.secret("a-keys", "opcert")
	h.pool("a", v1alpha1.CardanoNode, v1alpha1.TierForging, nil)
	h.startOperator()
	name := Name(h.get("a"))
	old := h.running(PodName(name), v1alpha1.CardanoNode)
	h.kube.Withhold(old.Name, true)
	h.edit("a", func(p *v1alpha1.ContinuumPool) { p.Spec.Implementation = v1alpha1.Dingo })
	h.eventually("old pod terminating", func() bool {
		p, ok := h.pod(old.Name)
		return ok && p.DeletionTimestamp != nil
	})
	h.consistently("no dingo workload while the old pod terminates", 1500*time.Millisecond, func() bool {
		s, ok := h.set(name)
		p, _ := h.pod(old.Name)
		return (!ok || s.Labels[LabelImpl] != string(v1alpha1.Dingo)) && p != nil && p.UID == old.UID
	})
	h.kube.Withhold(old.Name, false)
	h.running(PodName(name), v1alpha1.Dingo)
}

// TestFinalizerHeldOnNotReadyNode: ForgingLostIsGoneBug and
// ForgingNoFinalizerBug. A force delete on an unreachable node leaves the
// object, and the pool names the pod and the node (rule 8).
func TestFinalizerHeldOnNotReadyNode(t *testing.T) {
	h := newHarness(t, nil)
	h.secret("a-keys", "opcert")
	h.pool("a", v1alpha1.CardanoNode, v1alpha1.TierForging, nil)
	h.startOperator()
	old := h.running(PodName(Name(h.get("a"))), v1alpha1.CardanoNode)
	h.kube.Lose()
	h.must(fakekube.SetReady(h.ctx, h.c, h.node, false))
	h.must(h.c.Delete(h.ctx, old, client.GracePeriodSeconds(0)))
	h.eventually("fence condition naming pod and node", func() bool {
		return h.hasCondition("a", CondFence, metav1.ConditionTrue, ReasonNodeUnreachable, old.Name, h.node)
	})
	h.consistently("pod object held", time.Second, func() bool {
		p, ok := h.pod(old.Name)
		return ok && p.UID == old.UID
	})
}

func TestFinalizerReleasedOnTerminatedStatus(t *testing.T) {
	h := newHarness(t, nil)
	h.secret("a-keys", "opcert")
	h.pool("a", v1alpha1.CardanoNode, v1alpha1.TierForging, nil)
	h.startOperator()
	old := h.running(PodName(Name(h.get("a"))), v1alpha1.CardanoNode)
	h.must(h.c.Delete(h.ctx, old))
	h.eventually("old pod released and gone", func() bool { return h.goneUID(old.Name, old.UID) })
}

func TestFinalizerReleasedOnNodeDeleted(t *testing.T) {
	h := newHarness(t, nil)
	h.secret("a-keys", "opcert")
	h.pool("a", v1alpha1.CardanoNode, v1alpha1.TierForging, nil)
	h.startOperator()
	old := h.running(PodName(Name(h.get("a"))), v1alpha1.CardanoNode)
	h.kube.Lose()
	h.must(fakekube.SetReady(h.ctx, h.c, h.node, false))
	h.must(h.c.Delete(h.ctx, old))
	h.consistently("pod object held while the node exists", 500*time.Millisecond, func() bool {
		return !h.goneUID(old.Name, old.UID)
	})
	h.must(h.c.Delete(h.ctx, &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: h.node}}))
	h.eventually("old pod released and gone", func() bool { return h.goneUID(old.Name, old.UID) })
}

// TestFinalizerPatchHasUIDPrecondition: rule 6. A release decided on an old
// pod never reaches the new pod of the same name.
func TestFinalizerPatchHasUIDPrecondition(t *testing.T) {
	h := newHarness(t, nil)
	mk := func() *corev1.Pod {
		p := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "p-0", Namespace: h.ns, Finalizers: []string{Finalizer}},
			Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "node", Image: "x"}}},
		}
		h.must(h.c.Create(h.ctx, p))
		return p
	}
	old := mk()
	h.must(h.c.Delete(h.ctx, old))
	stale := &corev1.Pod{}
	h.must(h.c.Get(h.ctx, client.ObjectKeyFromObject(old), stale))
	clear := stale.DeepCopy()
	clear.Finalizers = nil
	h.must(h.c.Update(h.ctx, clear))
	h.eventually("old pod gone", func() bool { _, ok := h.pod("p-0"); return !ok })
	fresh := mk()

	r := NewRelease(h.c, h.c, quiet())
	if err := r.release(h.ctx, stale.DeepCopy()); err == nil {
		t.Fatal("release of the old pod succeeded against the new one")
	}
	now, _ := h.pod("p-0")
	if now.UID != fresh.UID || len(now.Finalizers) != 1 {
		t.Fatalf("new pod %s finalizers %v, want %s with the fence", now.UID, now.Finalizers, fresh.UID)
	}
	h.must(h.c.Delete(h.ctx, now))
	h.must(h.c.Get(h.ctx, client.ObjectKeyFromObject(now), now))
	h.must(r.release(h.ctx, now))
	h.eventually("new pod released", func() bool { _, ok := h.pod("p-0"); return !ok })
}

// laggy serves ContinuumPool reads from a snapshot refreshed only every
// lag, as a reconciler on a stale informer does.
type laggy struct {
	client.Client
	lag time.Duration

	mu sync.Mutex
	// protects at, snap
	at   map[types.NamespacedName]time.Time
	snap map[types.NamespacedName]*v1alpha1.ContinuumPool
}

func (l *laggy) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	p, ok := obj.(*v1alpha1.ContinuumPool)
	if !ok {
		return l.Client.Get(ctx, key, obj, opts...)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if s := l.snap[key]; s != nil && time.Since(l.at[key]) < l.lag {
		s.DeepCopyInto(p)
		return nil
	}
	if err := l.Client.Get(ctx, key, p, opts...); err != nil {
		return err
	}
	l.snap[key], l.at[key] = p.DeepCopy(), time.Now()
	return nil
}

// TestTwoReconcilersOneForger: ForgingOverlap and ForgingFencePerDriverBug.
// Two managers with no leader election, one on a stale view, go through
// go-live, a switch and go-dark; there is never a second keyed pod.
func TestTwoReconcilersOneForger(t *testing.T) {
	h := newHarness(t, nil)
	h.secret("a-keys", "opcert")
	h.startOperator()
	stale := h.manager()
	h.must(Index(h.ctx, stale))
	lag := &laggy{Client: stale.GetClient(), lag: time.Second, at: map[types.NamespacedName]time.Time{}, snap: map[types.NamespacedName]*v1alpha1.ContinuumPool{}}
	h.must(NewPools(lag, Drivers(), fakeResolver{}, quiet()).SetupWithManager(stale))
	h.must(NewRelease(stale.GetClient(), stale.GetAPIReader(), quiet()).SetupWithManager(stale))
	h.run("stale manager", stale.Start)

	var most int
	var mu sync.Mutex
	h.run("monitor", func(ctx context.Context) error {
		for ctx.Err() == nil {
			n := h.keyedPods(RoleForger)
			mu.Lock()
			most = max(most, n)
			mu.Unlock()
			h.pause()
		}
		return nil
	})

	h.pool("a", v1alpha1.CardanoNode, v1alpha1.TierForging, nil)
	name := Name(h.get("a"))
	h.running(PodName(name), v1alpha1.CardanoNode)
	h.edit("a", func(p *v1alpha1.ContinuumPool) { p.Spec.Implementation = v1alpha1.Dingo })
	h.running(PodName(name), v1alpha1.Dingo)
	h.edit("a", func(p *v1alpha1.ContinuumPool) { p.Spec.Dark = true })
	h.eventually("dark: no keyed pod", func() bool { return h.keyedPods(RoleForger) == 0 })

	mu.Lock()
	defer mu.Unlock()
	if most > 1 {
		t.Fatalf("%d keyed pods at once", most)
	}
}

// scripted returns the pool versions in order, one per Get, then the last.
type scripted struct {
	client.Client
	pools []*v1alpha1.ContinuumPool
}

func (s *scripted) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	p, ok := obj.(*v1alpha1.ContinuumPool)
	if !ok || key.Name != s.pools[0].Name {
		return s.Client.Get(ctx, key, obj, opts...)
	}
	s.pools[0].DeepCopyInto(p)
	if len(s.pools) > 1 {
		s.pools = s.pools[1:]
	}
	return nil
}

// TestStaleSpecDoesNotFlap: ForgingGenerationRaceBug. An older spec read
// after a newer one is dropped, not acted on.
func TestStaleSpecDoesNotFlap(t *testing.T) {
	h := newHarness(t, nil)
	h.secret("a-keys", "opcert")
	h.pool("a", v1alpha1.CardanoNode, v1alpha1.TierForging, nil)
	live := h.get("a")
	h.edit("a", func(p *v1alpha1.ContinuumPool) { p.Spec.Dark = true })
	dark := h.get("a")
	if dark.Generation <= live.Generation {
		t.Fatalf("generations %d then %d", live.Generation, dark.Generation)
	}
	r := NewPools(&scripted{Client: h.c, pools: []*v1alpha1.ContinuumPool{dark, live}}, Drivers(), fakeResolver{}, quiet())
	if _, err := r.Reconcile(h.ctx, request(dark)); err != nil {
		t.Fatal(err)
	}
	replicas := func() int32 {
		s, ok := h.set(Name(dark))
		if !ok {
			t.Fatal("no StatefulSet")
		}
		return *s.Spec.Replicas
	}
	if got := replicas(); got != 0 {
		t.Fatalf("dark spec: %d replicas", got)
	}
	res, err := r.Reconcile(h.ctx, request(dark))
	if err != nil {
		t.Fatal(err)
	}
	if got := replicas(); got != 0 {
		t.Fatalf("older live spec acted on: %d replicas", got)
	}
	if res.RequeueAfter == 0 {
		t.Error("stale spec was not requeued")
	}
}

// TestDuplicateUsesOwnSecret: rule 4, across implementations: a dingo
// duplicate of a cardano-node pool.
func TestDuplicateUsesOwnSecret(t *testing.T) {
	h := newHarness(t, nil)
	h.secret("a-keys", "opcert")
	h.secret("d-keys", "opcert")
	h.pool("a", v1alpha1.CardanoNode, v1alpha1.TierForging, nil)
	h.pool("d", v1alpha1.Dingo, v1alpha1.TierForging, func(p *v1alpha1.ContinuumPool) { p.Spec.UnsafeDuplicateOf = "a" })
	h.startOperator()
	a, d := Name(h.get("a")), Name(h.get("d"))
	if a == d {
		t.Fatalf("duplicate shares the name %s", a)
	}
	pa := h.running(PodName(a), v1alpha1.CardanoNode)
	pd := h.running(PodName(d), v1alpha1.Dingo)
	if !hasVolumeFrom(pa.Spec, "a-keys") || hasVolumeFrom(pa.Spec, "d-keys") {
		t.Error("forger does not mount only its own Secret")
	}
	if !hasVolumeFrom(pd.Spec, "d-keys") || hasVolumeFrom(pd.Spec, "a-keys") {
		t.Error("duplicate does not mount only its own Secret")
	}
	if pa.Labels[LabelPool] != pd.Labels[LabelPool] {
		t.Error("duplicate is not listed under the pool's label")
	}
	if pa.Labels[LabelImpl] != string(v1alpha1.CardanoNode) || pd.Labels[LabelImpl] != string(v1alpha1.Dingo) {
		t.Errorf("forger runs %s and duplicate %s", pa.Labels[LabelImpl], pd.Labels[LabelImpl])
	}
	cm := &corev1.ConfigMap{}
	h.must(h.c.Get(h.ctx, client.ObjectKey{Namespace: h.ns, Name: configName(d)}, cm))
	if !strings.Contains(cm.Data[dingoConfigFile], KeysDir+"/"+KESKey) {
		t.Errorf("dingo duplicate's config does not read its keys at %s:\n%s", KeysDir, cm.Data[dingoConfigFile])
	}
	h.pool("s", v1alpha1.Dingo, v1alpha1.TierForging, func(p *v1alpha1.ContinuumPool) {
		p.Spec.UnsafeDuplicateOf = "a"
		p.Spec.KeysSecret = "a-keys"
	})
	h.eventually("shared-secret duplicate refused", func() bool {
		return h.hasCondition("s", CondAdmitted, metav1.ConditionFalse, ReasonSharedSecret)
	})
	if _, ok := h.set(Name(h.get("s"))); ok {
		t.Fatal("shared-secret duplicate has a workload")
	}
}

// TestDuplicatePendingWithoutSecret: ForgingDuplicateSharedSecretBug. A
// duplicate created after its Secret was deleted never starts and goes.
func TestDuplicatePendingWithoutSecret(t *testing.T) {
	h := newHarness(t, nil)
	h.secret("a-keys", "opcert")
	h.pool("a", v1alpha1.CardanoNode, v1alpha1.TierForging, nil)
	d := h.pool("d", v1alpha1.Dingo, v1alpha1.TierForging, func(p *v1alpha1.ContinuumPool) { p.Spec.UnsafeDuplicateOf = "a" })
	set := h.renderDirect(h.get(d.Name))
	notRunning := func() bool {
		p, ok := h.pod(PodName(set.Name))
		return !ok || p.Status.Phase != corev1.PodRunning
	}
	h.eventually("duplicate pod pending", func() bool {
		p, ok := h.pod(PodName(set.Name))
		return ok && p.Status.Phase == corev1.PodPending
	})
	h.consistently("duplicate pod never runs", 500*time.Millisecond, notRunning)
	h.startOperator()
	h.eventually("duplicate workload deleted", func() bool {
		_, sok := h.set(set.Name)
		_, pok := h.pod(PodName(set.Name))
		return !sok && !pok && notRunning()
	})
	if !h.hasCondition("d", CondAdmitted, metav1.ConditionFalse, ReasonSecretMissing, "d-keys") {
		t.Fatalf("condition %+v", h.condition("d", CondAdmitted))
	}
}

// TestLatePodAfterRetireCannotStart: ForgingProbeLatePodBug. A keyed pod
// created for a retired (dark, blank Secret) pool stays pending and goes.
func TestLatePodAfterRetireCannotStart(t *testing.T) {
	h := newHarness(t, nil)
	h.secret("a-keys", "")
	p := h.pool("a", v1alpha1.CardanoNode, v1alpha1.TierForging, func(p *v1alpha1.ContinuumPool) { p.Spec.Dark = true })
	late := h.get(p.Name)
	late.Spec.Dark = false
	set := h.renderDirect(late)
	pod := PodName(set.Name)
	h.eventually("late pod pending", func() bool {
		p, ok := h.pod(pod)
		return ok && p.Status.Phase == corev1.PodPending
	})
	h.consistently("late pod never runs", 500*time.Millisecond, func() bool {
		p, ok := h.pod(pod)
		return !ok || p.Status.Phase != corev1.PodRunning
	})
	h.startOperator()
	h.eventually("late pod deleted", func() bool {
		_, ok := h.pod(pod)
		s, sok := h.set(set.Name)
		return !ok && sok && *s.Spec.Replicas == 0
	})
}
