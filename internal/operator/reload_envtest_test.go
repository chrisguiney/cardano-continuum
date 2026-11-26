//go:build envtest

package operator

import (
	"context"
	"fmt"
	"path"
	"slices"
	"strings"
	"sync"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/chrisguiney/cardano-continuum/api/v1alpha1"
)

// kubeletExec plays a kubelet that has projected every ConfigMap: cat reads
// the pod's Config from the API server. It records, per pod, the topology
// each reload command saw.
type kubeletExec struct {
	c client.Client

	mu sync.Mutex
	// protects loaded
	loaded map[string][]string
}

func (e *kubeletExec) Exec(ctx context.Context, pod types.NamespacedName, _ string, argv []string) ([]byte, error) {
	var p corev1.Pod
	if err := e.c.Get(ctx, pod, &p); err != nil {
		return nil, err
	}
	var cm corev1.ConfigMap
	key := types.NamespacedName{Namespace: pod.Namespace, Name: configName(p.Labels[LabelWorkload])}
	if err := e.c.Get(ctx, key, &cm); err != nil {
		return nil, err
	}
	if argv[0] == "cat" {
		return []byte(cm.Data[path.Base(argv[1])]), nil
	}
	if !slices.Equal(argv, cardanoReload) {
		return nil, fmt.Errorf("unexpected command %q", argv)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.loaded[pod.Name] = append(e.loaded[pod.Name], cm.Data[cardanoTopology])
	return nil, nil
}

func (e *kubeletExec) last(pod string) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	l := e.loaded[pod]
	if len(l) == 0 {
		return ""
	}
	return l[len(l)-1]
}

// TestFirstPoolLearnsLaterPeers: a pool whose pod starts before its peers
// exist reads a topology without them. Once they exist its node must be
// told to reread the topology, or it never dials them and forges alone.
func TestFirstPoolLearnsLaterPeers(t *testing.T) {
	h := newHarness(t, nil)
	exec := &kubeletExec{c: h.c, loaded: map[string][]string{}}
	h.startOperatorWith(exec)
	first := h.pool("a", v1alpha1.CardanoNode, v1alpha1.TierValidating, func(p *v1alpha1.ContinuumPool) { p.Spec.PoolID = "pool1a" })
	h.running(PodName(Name(h.get(first.Name))), v1alpha1.CardanoNode)
	second := h.pool("b", v1alpha1.CardanoNode, v1alpha1.TierValidating, func(p *v1alpha1.ContinuumPool) { p.Spec.PoolID = "pool1b" })
	peer := Name(h.get(second.Name)) + "." + h.ns + ".svc."
	pod := PodName(Name(h.get(first.Name)))
	h.eventually(pod+" reloads a topology naming "+peer, func() bool {
		return strings.Contains(exec.last(pod), peer)
	})
}
