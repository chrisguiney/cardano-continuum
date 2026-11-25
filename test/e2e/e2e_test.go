//go:build e2e

// Package e2e runs continuum against a kind cluster started by `make
// kind-up`. Run it with `make e2e`.
//
// Every kubectl call is pinned to the e2e cluster's context so a stray run
// cannot touch the developer's own cluster. Waits poll with a deadline and
// never sleep blindly.
package e2e

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// kubeContext is the context kind creates for the cluster named in
// hack/kind-up.sh.
const kubeContext = "kind-continuum-e2e"

// nsPrefix prefixes every namespace the tests create.
const nsPrefix = "continuum-e2e-"

// repoRoot is the module root; tests run in test/e2e.
func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// kubectl runs kubectl against the e2e cluster with stdin as its input.
func kubectl(ctx context.Context, stdin string, args ...string) (string, error) {
	full := append([]string{"--context", kubeContext}, args...)
	cmd := exec.CommandContext(ctx, "kubectl", full...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return out.String(), fmt.Errorf("kubectl %s: %w: %s", args[0], err, strings.TrimSpace(errb.String()))
	}
	return out.String(), nil
}

// kube is kubectl that fails the test on error.
func kube(t *testing.T, stdin string, args ...string) string {
	t.Helper()
	out, err := kubectl(t.Context(), stdin, args...)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// poll calls check every interval until it reports done, errors, or timeout
// passes. check errors that mean "not yet" should be returned as (false, nil).
func poll(t *testing.T, timeout, interval time.Duration, what string, check func(context.Context) (bool, error)) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), timeout)
	defer cancel()
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		done, err := check(ctx)
		if err != nil {
			t.Fatalf("wait for %s: %v", what, err)
		}
		if done {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("timed out after %s waiting for %s", timeout, what)
		case <-tick.C:
		}
	}
}

var notDNS = regexp.MustCompile(`[^a-z0-9]+`)

// newNS creates a fresh namespace for t and removes it when t ends. A failed
// test dumps the namespace's pods, events and logs first.
func newNS(t *testing.T) string {
	t.Helper()
	ns := nsPrefix + strings.Trim(notDNS.ReplaceAllString(strings.ToLower(t.Name()), "-"), "-")
	if len(ns) > 63 {
		ns = ns[:63]
	}
	ns = strings.TrimRight(ns, "-")
	// A previous failed run may have left the namespace behind.
	if _, err := kubectl(t.Context(), "", "delete", "namespace", ns, "--ignore-not-found", "--wait=true", "--timeout=120s"); err != nil {
		t.Fatal(err)
	}
	kube(t, "", "create", "namespace", ns)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 2*time.Minute)
		defer cancel()
		if t.Failed() {
			dump(ctx, t, ns)
		}
		_, _ = kubectl(ctx, "", "delete", "namespace", ns, "--ignore-not-found", "--wait=false")
	})
	return ns
}

func dump(ctx context.Context, t *testing.T, ns string) {
	for _, args := range [][]string{
		{"-n", ns, "get", "pods,jobs,pvc", "-o", "wide"},
		{"-n", ns, "get", "events", "--sort-by=.lastTimestamp"},
	} {
		out, err := kubectl(ctx, "", args...)
		t.Logf("kubectl %s:\n%s%v", strings.Join(args, " "), out, err)
	}
	pods, err := kubectl(ctx, "", "-n", ns, "get", "pods", "-o", "name")
	if err != nil {
		return
	}
	for _, pod := range strings.Fields(pods) {
		out, err := kubectl(ctx, "", "-n", ns, "logs", pod, "--all-containers", "--tail=40")
		t.Logf("%s logs:\n%s%v", pod, out, err)
	}
}

// mustEnv returns a required environment variable.
func mustEnv(t *testing.T, name string) string {
	t.Helper()
	v := os.Getenv(name)
	if v == "" {
		t.Fatalf("%s is not set; run the tests through make e2e", name)
	}
	return v
}
