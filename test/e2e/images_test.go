//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

var (
	imagesMu sync.Mutex // protects images
	images   = map[string]string{}
)

// koBuild builds ./cmd/<name> with ko, pushes it to CONTINUUM_REGISTRY and
// returns the digest reference the cluster pulls. Results are cached per
// process. The ko command comes from CONTINUUM_KO, which the Makefile pins.
func koBuild(t *testing.T, name string) string {
	t.Helper()
	imagesMu.Lock()
	defer imagesMu.Unlock()
	if ref, ok := images[name]; ok {
		return ref
	}
	registry := mustEnv(t, "CONTINUUM_REGISTRY")
	ko := strings.Fields(mustEnv(t, "CONTINUUM_KO"))
	args := append(ko[1:], "build", "--base-import-paths", "--platform=linux/"+runtime.GOARCH, "./cmd/"+name)
	if mustEnv(t, "CONTINUUM_REGISTRY_INSECURE") == "true" {
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

// registryURL is the host-side plain-HTTP URL of the e2e registry.
func registryURL(t *testing.T) string {
	t.Helper()
	host, _, _ := strings.Cut(mustEnv(t, "CONTINUUM_REGISTRY"), "/")
	return fmt.Sprintf("http://%s/v2/", host)
}
