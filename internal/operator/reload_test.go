package operator

import (
	"context"
	"io"
	"log/slog"
	"slices"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/chrisguiney/cardano-continuum/api/v1alpha1"
	"github.com/chrisguiney/cardano-continuum/internal/operator/render"
)

// fakeExec serves cat from files, the node container's view of its Config
// volume, and records every other command.
type fakeExec struct {
	files map[string]string
	ran   [][]string
}

func (e *fakeExec) Exec(_ context.Context, _ types.NamespacedName, container string, argv []string) ([]byte, error) {
	if container != render.NodeContainer {
		return nil, io.ErrUnexpectedEOF
	}
	if len(argv) == 2 && argv[0] == "cat" {
		return []byte(e.files[argv[1]]), nil
	}
	e.ran = append(e.ran, argv)
	return nil, nil
}

func (e *fakeExec) reloads() int {
	n := 0
	for _, argv := range e.ran {
		if slices.Equal(argv, []string{"kill", "-HUP", "1"}) {
			n++
		}
	}
	return n
}

type reloadRig struct {
	r    *reloader
	exec *fakeExec
	c    client.Client
	now  time.Time
	w    render.Workload
	pod  *corev1.Pod
}

func newReloadRig(t *testing.T) *reloadRig {
	t.Helper()
	s := runtime.NewScheme()
	if err := scheme.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "pool-x-0", Namespace: "ns", UID: "pod-uid"},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
			Name:  render.NodeContainer,
			State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(start)}},
		}}},
	}
	c := fake.NewClientBuilder().WithScheme(s).WithObjects(pod).Build()
	exec := &fakeExec{files: map[string]string{}}
	rig := &reloadRig{exec: exec, c: c, now: start, pod: pod}
	rig.r = &reloader{client: c, exec: exec, log: slog.New(slog.NewTextHandler(io.Discard, nil)), now: func() time.Time { return rig.now }}
	rig.w = renderOn(t, render.CardanoNode{}, network("ns"), pool("ns", "a", v1alpha1.CardanoNode, v1alpha1.TierForging))
	return rig
}

// step runs one reload against the stored pod, as a reconcile would.
func (g *reloadRig) step(t *testing.T) time.Duration {
	t.Helper()
	var pod corev1.Pod
	if err := g.c.Get(context.Background(), client.ObjectKeyFromObject(g.pod), &pod); err != nil {
		t.Fatal(err)
	}
	wait, err := g.r.reload(context.Background(), &pod, g.w)
	if err != nil {
		t.Fatal(err)
	}
	return wait
}

// project is the kubelet writing the rendered Config into the volume.
func (g *reloadRig) project() {
	for k, v := range g.w.Config.Data {
		g.exec.files[render.ConfigDir+"/"+k] = v
	}
}

// TestCardanoNodeRendersReload: cardano-node reads its topology once at
// start, so a pod started before its peers existed never dials them unless
// it is told to reread it.
func TestCardanoNodeRendersReload(t *testing.T) {
	w := renderOn(t, render.CardanoNode{}, network("ns"), pool("ns", "a", v1alpha1.CardanoNode, v1alpha1.TierForging))
	if !slices.Equal(w.Reload, []string{"kill", "-HUP", "1"}) {
		t.Fatalf("Reload = %q, want kill -HUP 1", w.Reload)
	}
}

// TestReloadWaitsForProjection: the node rereads its topology only once the
// kubelet has written the rendered one into the volume, and only once.
func TestReloadWaitsForProjection(t *testing.T) {
	g := newReloadRig(t)
	g.now = g.now.Add(time.Minute)
	g.exec.files[render.ConfigDir+"/topology.json"] = "stale"
	if wait := g.step(t); wait <= 0 || g.exec.reloads() != 0 {
		t.Fatalf("stale volume: wait %v, reloads %d; want a retry and no reload", wait, g.exec.reloads())
	}
	g.project()
	if wait := g.step(t); wait != 0 || g.exec.reloads() != 1 {
		t.Fatalf("projected volume: wait %v, reloads %d; want one reload", wait, g.exec.reloads())
	}
	g.step(t)
	if g.exec.reloads() != 1 {
		t.Fatalf("reloads %d after the node already loaded this config, want 1", g.exec.reloads())
	}
}

// TestReloadWaitsForStart: a signal sent before the node installs its
// handler is lost, so a just-started container is left alone for a while.
func TestReloadWaitsForStart(t *testing.T) {
	g := newReloadRig(t)
	g.project()
	g.now = g.now.Add(time.Second)
	if wait := g.step(t); wait <= 0 || g.exec.reloads() != 0 {
		t.Fatalf("just started: wait %v, reloads %d; want a retry and no reload", wait, g.exec.reloads())
	}
	g.now = g.now.Add(time.Minute)
	if g.step(t); g.exec.reloads() != 1 {
		t.Fatalf("reloads %d, want 1", g.exec.reloads())
	}
}

// TestReloadAfterConfigChange: a new peer changes the topology, and the
// running node must reread it.
func TestReloadAfterConfigChange(t *testing.T) {
	g := newReloadRig(t)
	g.now = g.now.Add(time.Minute)
	g.project()
	g.step(t)
	g.w.Config.Data["topology.json"] += " "
	g.project()
	if g.step(t); g.exec.reloads() != 2 {
		t.Fatalf("reloads %d after the topology changed, want 2", g.exec.reloads())
	}
}

// TestReloadAfterRestart: the annotation names the container instance it
// was recorded for, so a restarted container is checked again.
func TestReloadAfterRestart(t *testing.T) {
	g := newReloadRig(t)
	g.now = g.now.Add(time.Minute)
	g.project()
	g.step(t)
	var pod corev1.Pod
	if err := g.c.Get(context.Background(), client.ObjectKeyFromObject(g.pod), &pod); err != nil {
		t.Fatal(err)
	}
	pod.Status.ContainerStatuses[0].RestartCount = 1
	pod.Status.ContainerStatuses[0].State.Running.StartedAt = metav1.NewTime(g.now)
	if err := g.c.Status().Update(context.Background(), &pod); err != nil {
		t.Fatal(err)
	}
	g.now = g.now.Add(time.Minute)
	if g.step(t); g.exec.reloads() != 2 {
		t.Fatalf("reloads %d after a restart, want 2", g.exec.reloads())
	}
}

// TestReloadSkipsWithoutCommand: a driver with no reload command, or a pod
// whose node is not running, is never exec'd into.
func TestReloadSkipsWithoutCommand(t *testing.T) {
	g := newReloadRig(t)
	g.now = g.now.Add(time.Minute)
	g.project()
	g.w.Reload = nil
	g.step(t)
	if len(g.exec.ran) != 0 {
		t.Fatalf("ran %q for a driver without reload", g.exec.ran)
	}
}
