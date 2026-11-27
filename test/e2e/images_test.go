//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
)

var (
	imagesMu sync.Mutex // protects images
	images   = map[string]string{}
)

// koBuild builds ./cmd/<name> with ko for CONTINUUM_PLATFORM, pushes it to
// CONTINUUM_REGISTRY and returns the digest reference it pushed. Results
// are cached per process. The ko command comes from CONTINUUM_KO, which the
// Makefile pins.
func koBuild(t *testing.T, name string) string {
	t.Helper()
	imagesMu.Lock()
	defer imagesMu.Unlock()
	if ref, ok := images[name]; ok {
		return ref
	}
	registry := mustEnv(t, "CONTINUUM_REGISTRY")
	ko := strings.Fields(mustEnv(t, "CONTINUUM_KO"))
	args := append(ko[1:], "build", "--base-import-paths", "--platform="+mustEnv(t, "CONTINUUM_PLATFORM"), "./cmd/"+name)
	if os.Getenv("CONTINUUM_REGISTRY_INSECURE") == "true" {
		args = append(args, "--insecure-registry")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, ko[0], args...)
	cmd.Dir = repoRoot(t)
	cmd.Env = append(cmd.Environ(), "KO_DOCKER_REPO="+registry)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		t.Fatalf("ko build %s: %v\n%s", name, err, errb.String())
	}
	ref := strings.TrimSpace(out.String())
	if !strings.Contains(ref, "@sha256:") || strings.ContainsAny(ref, " \n") {
		t.Fatalf("ko build %s printed %q, want one digest reference", name, ref)
	}
	images[name] = ref
	return ref
}

// clusterRegistry is the registry as the cluster's nodes name it:
// CONTINUUM_E2E_CLUSTER_REGISTRY when the cluster reaches the registry under
// another name than this machine pushed to, else CONTINUUM_REGISTRY.
func clusterRegistry(t *testing.T) string {
	t.Helper()
	if r := os.Getenv("CONTINUUM_E2E_CLUSTER_REGISTRY"); r != "" {
		return r
	}
	return mustEnv(t, "CONTINUUM_REGISTRY")
}

// clusterRef is ref, pushed to CONTINUUM_REGISTRY, as the cluster pulls it.
func clusterRef(t *testing.T, ref string) string {
	t.Helper()
	rest, ok := strings.CutPrefix(ref, mustEnv(t, "CONTINUUM_REGISTRY"))
	if !ok {
		t.Fatalf("%s is not under CONTINUUM_REGISTRY", ref)
	}
	return clusterRegistry(t) + rest
}

// digestOf is the sha256:... part of a digest reference.
func digestOf(ref string) string {
	_, d, _ := strings.Cut(ref, "@")
	return d
}

// TestImagePull proves the cluster pulls what this machine pushed: a
// ko-built image is pushed and a pod pulls and runs it. Every other test
// depends on that, so a registry the nodes cannot reach fails here first.
func TestImagePull(t *testing.T) {
	ref := clusterRef(t, koBuild(t, "continuum-genesis"))
	ns := newNS(t)
	// No arguments: the binary prints its usage and exits non-zero, which is
	// enough to show the image was pulled and started.
	kube(t, podYAML("probe", ref), "-n", ns, "apply", "-f", "-")
	poll(t, 3*time.Minute, 2*time.Second, "probe pod to run", func(ctx context.Context) (bool, error) {
		out, err := kubectl(ctx, "", "-n", ns, "get", "pod", "probe", "-o", "json")
		if err != nil {
			return false, err
		}
		var p corev1.Pod
		if err := json.Unmarshal([]byte(out), &p); err != nil {
			return false, err
		}
		for _, cs := range p.Status.ContainerStatuses {
			if w := cs.State.Waiting; w != nil && (w.Reason == "ErrImagePull" || w.Reason == "ImagePullBackOff") {
				t.Fatalf("pod cannot pull %s: %s: %s", ref, w.Reason, w.Message)
			}
			if cs.State.Terminated != nil {
				return true, nil
			}
		}
		return false, nil
	})
}

func podYAML(name, image string) string {
	return `apiVersion: v1
kind: Pod
metadata:
  name: ` + name + `
spec:
  restartPolicy: Never
  containers:
    - name: main
      image: ` + image + `
`
}
