//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
)

// TestKindUp proves the cluster and its plain-HTTP registry work together: a
// ko-built image is pushed over HTTP and a pod pulls and runs it.
func TestKindUp(t *testing.T) {
	ref := koBuild(t, "continuum-genesis")
	catalog := httpGet(t, strings.TrimSuffix(registryURL(t), "/")+"/_catalog")
	if !strings.Contains(catalog, "continuum-genesis") {
		t.Fatalf("registry catalog %q does not list continuum-genesis", catalog)
	}

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

func httpGet(t *testing.T, url string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		t.Fatalf("read %s: %v", url, err)
	}
	return string(body)
}
