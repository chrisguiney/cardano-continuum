//go:build envtest

package operator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"go.uber.org/goleak"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/chrisguiney/cardano-continuum/api/v1alpha1"
	"github.com/chrisguiney/cardano-continuum/internal/operator/fakekube"
	"github.com/chrisguiney/cardano-continuum/internal/operator/render"
)

// suite is the one API server every envtest test shares; each test works in
// its own namespace with its own Node.
var suite struct {
	cfg    *rest.Config
	scheme *kruntime.Scheme
	c      client.Client
}

func TestMain(m *testing.M) {
	os.Exit(runSuite(m))
}

func runSuite(m *testing.M) int {
	ctrllog.SetLogger(logr.Discard())
	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{"../../config/crd/bases"},
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := env.Start()
	if err != nil {
		fmt.Fprintln(os.Stderr, "start envtest (run make test-envtest):", err)
		return 1
	}
	defer func() { _ = env.Stop() }()
	s := kruntime.NewScheme()
	if err := errors.Join(clientgoscheme.AddToScheme(s), v1alpha1.AddToScheme(s)); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	c, err := client.New(cfg, client.Options{Scheme: s})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	suite.cfg, suite.scheme, suite.c = cfg, s, c
	before := goleak.IgnoreCurrent()
	if code := m.Run(); code != 0 {
		return code
	}
	// controller-runtime wraps a metadata-only informer's watch in
	// watch.Filter (cache/internal/informers.go, newGVKFixupWatcher). When a
	// manager stops, the reflector stops reading and Stop does not unblock
	// the filter's pending send, so that goroutine outlives every manager.
	// client-go shares one transport between every client built from the
	// suite's config, so its pooled connections belong to the suite, not to
	// a test, and stay open until the process exits.
	if err := goleak.Find(before,
		goleak.IgnoreTopFunction("k8s.io/apimachinery/pkg/watch.(*filteredWatch).loop"),
		goleak.IgnoreAnyFunction("net/http/internal/http2.(*ClientConn).readLoop"),
		goleak.IgnoreAnyFunction("net/http.(*persistConn).readLoop"),
		goleak.IgnoreAnyFunction("net/http.(*persistConn).writeLoop"),
	); err != nil {
		fmt.Fprintln(os.Stderr, "goroutines left after the tests:", err)
		return 1
	}
	return 0
}

// fakeResolver pins every reference to a digest derived from its text.
type fakeResolver struct{}

func (fakeResolver) Resolve(_ context.Context, ref string) (v1alpha1.ResolvedImage, error) {
	h := sha256.Sum256([]byte(ref))
	return v1alpha1.ResolvedImage{Ref: ref, Digest: "sha256:" + hex.EncodeToString(h[:])}, nil
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

type harness struct {
	t    *testing.T
	ctx  context.Context
	ns   string
	node string
	c    client.Client
	kube *fakekube.Cluster
}

// newHarness creates a namespace, its network and genesis ConfigMap, and
// starts the fake cluster. edit, if not nil, adjusts the network first.
func newHarness(t *testing.T, edit func(*v1alpha1.ContinuumNetwork)) *harness {
	t.Helper()
	// h.ctx is not t.Context(): that is cancelled before any cleanup runs,
	// so a goroutine started with run would see its API calls fail before
	// run's cleanup has told it to stop. Registered first, this cancel runs
	// last, after every run goroutine has been joined.
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	h := &harness{t: t, ctx: ctx, c: suite.c}
	sum := sha256.Sum256([]byte(t.Name()))
	h.ns = "t-" + hex.EncodeToString(sum[:6])
	h.node = h.ns + "-node"
	h.must(h.c.Create(h.ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: h.ns}}))
	h.must(h.c.Create(h.ctx, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "genesis", Namespace: h.ns}, Data: map[string]string{"config.json": "{}"}}))
	n := testNetwork(h.ns)
	if edit != nil {
		edit(n)
	}
	h.must(h.c.Create(h.ctx, n))
	h.kube = fakekube.New(h.c, h.ns, h.node, nil)
	h.run("fakekube", h.kube.Run)
	return h
}

// run starts f in a goroutine and, at cleanup, cancels and joins it. The
// join is bounded: a goroutine that does not return within patience fails
// the test with every goroutine's stack. f must not call t.FailNow (h.must,
// t.Fatal): off the test goroutine that ends f without returning, which run
// reports instead of waiting for.
func (h *harness) run(what string, f func(context.Context) error) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	exited := make(chan struct{})
	go func() {
		defer close(exited)
		done <- f(ctx)
	}()
	h.t.Cleanup(func() {
		cancel()
		timer := time.NewTimer(patience)
		defer timer.Stop()
		select {
		case <-exited:
		case <-timer.C:
			h.t.Errorf("%s still running %v after cancel:\n%s", what, patience, stacks())
			return
		}
		select {
		case err := <-done:
			if err != nil {
				h.t.Errorf("%s: %v", what, err)
			}
		default:
			h.t.Errorf("%s exited without returning (runtime.Goexit, e.g. t.FailNow off the test goroutine)", what)
		}
	})
}

func stacks() []byte {
	buf := make([]byte, 1<<20)
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			return buf[:n]
		}
		buf = make([]byte, 2*len(buf))
	}
}

// TestHarnessJoinsBeforeCancel: a goroutine started with run may call the
// harness's API helpers until it is told to stop, including the call it is
// making when teardown begins.
func TestHarnessJoinsBeforeCancel(t *testing.T) {
	h := newHarness(t, nil)
	h.run("probe", func(ctx context.Context) error {
		<-ctx.Done()
		h.keyedPods(render.RoleForger)
		return nil
	})
}

func (h *harness) must(err error) {
	h.t.Helper()
	if err != nil {
		h.t.Fatal(err)
	}
}

// manager returns a manager restricted to the test namespace. Two managers
// in one process need SkipNameValidation.
func (h *harness) manager() ctrl.Manager {
	h.t.Helper()
	opts, err := CacheOptions()
	h.must(err)
	opts.DefaultNamespaces = map[string]cache.Config{h.ns: {}}
	mgr, err := ctrl.NewManager(suite.cfg, ctrl.Options{
		Scheme:                 suite.scheme,
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
		Cache:                  opts,
		Controller:             config.Controller{SkipNameValidation: ptr.To(true)},
	})
	h.must(err)
	return mgr
}

// startOperator runs the whole operator, as Setup wires it.
func (h *harness) startOperator() { h.startOperatorWith(nil) }

// startOperatorWith runs the operator with exec into pods.
func (h *harness) startOperatorWith(exec Exec) {
	h.t.Helper()
	mgr := h.manager()
	h.must(Setup(h.ctx, mgr, Options{Drivers: render.Drivers(), Images: fakeResolver{}, Exec: exec, Log: quiet()}))
	h.run("manager", mgr.Start)
}

func (h *harness) secret(name, opcert string) {
	h.t.Helper()
	h.must(h.c.Create(h.ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: h.ns},
		Data:       map[string][]byte{render.KESKey: []byte("kes"), render.VRFKey: []byte("vrf"), render.OpCert: []byte(opcert)},
	}))
}

// pool creates a pool for PoolID "pool1abc"; forging pools get <name>-keys.
func (h *harness) pool(name string, impl v1alpha1.Implementation, tier v1alpha1.Tier, edit func(*v1alpha1.ContinuumPool)) *v1alpha1.ContinuumPool {
	h.t.Helper()
	p := pool(h.ns, name, impl, tier)
	p.UID = ""
	if edit != nil {
		edit(p)
	}
	h.must(h.c.Create(h.ctx, p))
	return p
}

func (h *harness) get(name string) *v1alpha1.ContinuumPool {
	h.t.Helper()
	var p v1alpha1.ContinuumPool
	h.must(h.c.Get(h.ctx, types.NamespacedName{Namespace: h.ns, Name: name}, &p))
	return &p
}

// edit retries f on a fresh copy of pool name until the update lands.
func (h *harness) edit(name string, f func(*v1alpha1.ContinuumPool)) {
	h.t.Helper()
	h.eventually("update pool "+name, func() bool {
		p := h.get(name)
		f(p)
		return h.c.Update(h.ctx, p) == nil
	})
}

const patience = 20 * time.Second

func (h *harness) eventually(what string, f func() bool) {
	h.t.Helper()
	deadline := time.Now().Add(patience)
	for !f() {
		if time.Now().After(deadline) {
			h.t.Fatalf("timed out waiting for %s", what)
		}
		h.pause()
	}
}

// consistently fails if f is false at any poll during d.
func (h *harness) consistently(what string, d time.Duration, f func() bool) {
	h.t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if !f() {
			h.t.Fatalf("%s stopped holding", what)
		}
		h.pause()
	}
}

func (h *harness) pause() {
	t := time.NewTimer(10 * time.Millisecond)
	defer t.Stop()
	select {
	case <-h.ctx.Done():
	case <-t.C:
	}
}

func (h *harness) set(name string) (*appsv1.StatefulSet, bool) {
	var s appsv1.StatefulSet
	err := h.c.Get(h.ctx, types.NamespacedName{Namespace: h.ns, Name: name}, &s)
	if apierrors.IsNotFound(err) {
		return nil, false
	}
	h.must(err)
	return &s, true
}

func (h *harness) pod(name string) (*corev1.Pod, bool) {
	var p corev1.Pod
	err := h.c.Get(h.ctx, types.NamespacedName{Namespace: h.ns, Name: name}, &p)
	if apierrors.IsNotFound(err) {
		return nil, false
	}
	h.must(err)
	return &p, true
}

// running waits until pod name runs implementation impl and returns it.
func (h *harness) running(name string, impl v1alpha1.Implementation) *corev1.Pod {
	h.t.Helper()
	var pod *corev1.Pod
	h.eventually(fmt.Sprintf("%s running %s", name, impl), func() bool {
		p, ok := h.pod(name)
		pod = p
		return ok && p.DeletionTimestamp == nil && p.Status.Phase == corev1.PodRunning && p.Labels[render.LabelImpl] == string(impl)
	})
	return pod
}

// goneUID reports whether no pod with uid is left under name.
func (h *harness) goneUID(name string, uid types.UID) bool {
	p, ok := h.pod(name)
	return !ok || p.UID != uid
}

func (h *harness) condition(pool, typ string) *metav1.Condition {
	return meta.FindStatusCondition(h.get(pool).Status.Conditions, typ)
}

// hasCondition reports whether pool has condition typ with status and a
// reason and message containing the given text.
func (h *harness) hasCondition(pool, typ string, status metav1.ConditionStatus, reason string, words ...string) bool {
	c := h.condition(pool, typ)
	if c == nil || c.Status != status || c.Reason != reason {
		return false
	}
	for _, w := range words {
		if !strings.Contains(c.Message, w) {
			return false
		}
	}
	return true
}

// keyedPods counts the pod objects of PoolID "pool1abc" in role.
func (h *harness) keyedPods(role render.Role) int {
	var pods corev1.PodList
	h.must(h.c.List(h.ctx, &pods, client.InNamespace(h.ns), client.MatchingLabels{render.LabelPool: render.PoolKey("pool1abc"), render.LabelRole: string(role)}))
	return len(pods.Items)
}

// renderDirect creates p's StatefulSet as a stale reconciler would have,
// without running the operator.
func (h *harness) renderDirect(p *v1alpha1.ContinuumPool) *appsv1.StatefulSet {
	h.t.Helper()
	d, err := render.Drivers().Driver(p.Spec.Implementation)
	h.must(err)
	img, _ := fakeResolver{}.Resolve(h.ctx, d.Image())
	w, err := render.Render(d, render.Input{Network: testNetwork(h.ns), Pool: p, Image: render.Image{Name: pin(img.Ref, img.Digest)}})
	h.must(err)
	h.must(h.c.Create(h.ctx, w.StatefulSet))
	return w.StatefulSet
}

func request(p *v1alpha1.ContinuumPool) reconcile.Request {
	return reconcile.Request{NamespacedName: types.NamespacedName{Namespace: p.Namespace, Name: p.Name}}
}

// mustOwned fails unless the Service and ConfigMap of workload name exist
// and are controlled by p.
func (h *harness) mustOwned(p *v1alpha1.ContinuumPool, name string) {
	h.t.Helper()
	for _, obj := range []client.Object{&corev1.Service{}, &corev1.ConfigMap{}} {
		key := types.NamespacedName{Namespace: h.ns, Name: name}
		if _, ok := obj.(*corev1.ConfigMap); ok {
			key.Name = render.ConfigName(name)
		}
		h.must(h.c.Get(h.ctx, key, obj))
		if ref := metav1.GetControllerOf(obj); ref == nil || ref.UID != p.UID {
			h.t.Fatalf("%T %s controller %v, want pool %s", obj, key.Name, ref, p.UID)
		}
	}
}
