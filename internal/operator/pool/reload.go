package pool

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/chrisguiney/cardano-continuum/internal/operator/render"
)

// Exec runs argv, with no shell, in one container of a pod and returns its
// standard output.
type Exec interface {
	Exec(ctx context.Context, pod types.NamespacedName, container string, argv []string) ([]byte, error)
}

const (
	// reloadSettle is how long a node container runs before it is told to
	// reread its Config. A node ignores the signal until it has installed
	// its handler, which it does just after reading its topology, so a
	// signal sent in the first moments could be lost with no retry.
	reloadSettle = 10 * time.Second
	// reloadRetry paces the wait for the kubelet to project an updated
	// ConfigMap, which takes up to about a minute.
	reloadRetry = 5 * time.Second
)

// reloader tells running nodes to reread their Config.
type reloader struct {
	client client.Client
	// exec is nil when running nodes are never told to reread.
	exec Exec
	log  *slog.Logger
	now  func() time.Time
}

// reload makes pod's node reread w.Config once the kubelet has projected
// it, at most once per Config per container instance, and returns how soon
// to look again.
//
// A ConfigMap volume is updated in place, but a node reads its topology
// only at start. Pools of a network are created one by one, so the first
// pod starts with a topology that lacks the pools created after it; peers
// then dial it, but it never dials them, takes none of their blocks and
// forges its own fork.
func (r *reloader) reload(ctx context.Context, pod *corev1.Pod, w render.Workload) (time.Duration, error) {
	if r.exec == nil || len(w.Reload) == 0 || pod == nil || w.Config == nil {
		return 0, nil
	}
	instance, started, ok := nodeInstance(pod)
	if !ok {
		// The pod watch reconciles again once the container runs.
		return 0, nil
	}
	mark := instance + " " + configDigest(w.Config.Data)
	if pod.Annotations[render.AnnotationLoaded] == mark {
		return 0, nil
	}
	if wait := reloadSettle - r.now().Sub(started); wait > 0 {
		return wait, nil
	}
	key := client.ObjectKeyFromObject(pod)
	for _, f := range slices.Sorted(maps.Keys(w.Config.Data)) {
		got, err := r.exec.Exec(ctx, key, render.NodeContainer, []string{"cat", render.ConfigDir + "/" + f})
		if err != nil {
			return 0, fmt.Errorf("read %s in pod %s: %w", f, key, err)
		}
		if string(got) != w.Config.Data[f] {
			return reloadRetry, nil
		}
	}
	if _, err := r.exec.Exec(ctx, key, render.NodeContainer, w.Reload); err != nil {
		return 0, fmt.Errorf("reload node in pod %s: %w", key, err)
	}
	patch := client.MergeFrom(pod.DeepCopy())
	if pod.Annotations == nil {
		pod.Annotations = map[string]string{}
	}
	pod.Annotations[render.AnnotationLoaded] = mark
	if err := r.client.Patch(ctx, pod, patch); err != nil {
		return 0, fmt.Errorf("annotate pod %s: %w", key, err)
	}
	r.log.Info("node reread its config", "pod", pod.Name, "instance", instance)
	return 0, nil
}

// nodeInstance identifies the running node container of pod: its restart
// count names the instance within the pod.
func nodeInstance(pod *corev1.Pod) (string, time.Time, bool) {
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.Name != render.NodeContainer || cs.State.Running == nil {
			continue
		}
		return fmt.Sprintf("%s/%d", pod.UID, cs.RestartCount), cs.State.Running.StartedAt.Time, true
	}
	return "", time.Time{}, false
}

// configDigest is a short digest of a ConfigMap's data, in key order.
func configDigest(data map[string]string) string {
	h := sha256.New()
	for _, k := range slices.Sorted(maps.Keys(data)) {
		// A hash.Hash never returns a write error.
		_, _ = fmt.Fprintf(h, "%d:%s%d:%s", len(k), k, len(data[k]), data[k])
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// sooner is res, requeued after wait if that is earlier.
func sooner(res reconcile.Result, wait time.Duration) reconcile.Result {
	if wait > 0 && (res.RequeueAfter == 0 || wait < res.RequeueAfter) {
		res.RequeueAfter = wait
	}
	return res
}
