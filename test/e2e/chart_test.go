//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chrisguiney/cardano-continuum/internal/operator/render"
)

// releaseName is the Helm release every test installs in its namespace.
const releaseName = "continuum"

// release is the chart installed in one test's namespace.
type release struct {
	// ns is the namespace holding the release.
	ns string
	// sets are the --set arguments of the last install or upgrade, which an
	// upgrade repeats so that it changes only what it names.
	sets []string
}

// parts selects the chart's components.
type parts struct {
	// operator, genesis, network and engine enable the matching values.
	operator, genesis, network, engine bool
}

func (p parts) sets() []string {
	return []string{
		fmt.Sprintf("operator.enabled=%t", p.operator),
		fmt.Sprintf("genesis.enabled=%t", p.genesis),
		fmt.Sprintf("network.enabled=%t", p.network),
		fmt.Sprintf("engine.enabled=%t", p.engine),
	}
}

// helm runs CONTINUUM_HELM against the e2e context.
func helm(t *testing.T, args ...string) string {
	t.Helper()
	cmdline := strings.Fields(mustEnv(t, "CONTINUUM_HELM"))
	full := append(append(append([]string{}, cmdline[1:]...), "--kube-context", kubeContext), args...)
	ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 6*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, cmdline[0], full...)
	cmd.Dir = repoRoot(t)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		t.Fatalf("helm %s: %v: %s", args[0], err, strings.TrimSpace(errb.String()))
	}
	return out.String()
}

// installChart applies the chart's CRDs, which Helm never upgrades, and
// installs the chart into ns with the ko-built images and sets (key=value).
// When the test ends the pools go first, while the operator still runs, then
// the release.
func installChart(t *testing.T, ns string, p parts, sets ...string) *release {
	t.Helper()
	kube(t, "", "apply", "-f", filepath.Join(repoRoot(t), "charts", "continuum", "crds"))
	all := append([]string{
		"images.registry=" + clusterRegistry(t),
		"images.operator.digest=" + digestOf(koBuild(t, "continuum-operator")),
		"images.genesis.digest=" + digestOf(koBuild(t, "continuum-genesis")),
		"images.engine.digest=" + digestOf(koBuild(t, "continuum")),
		// The tests drive the engine's API directly, so they skip building
		// and running the UI image.
		"ui.enabled=false",
	}, p.sets()...)
	r := &release{ns: ns, sets: append(all, sets...)}
	r.helm(t, "install")
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 3*time.Minute)
		defer cancel()
		_, _ = kubectl(ctx, "", "-n", ns, "delete", "continuumpools", "--all", "--wait=true", "--timeout=60s")
		// Deleting a pool does not wait for its pod, whose fence finalizer
		// only the operator removes.
		_, _ = kubectl(ctx, "", "-n", ns, "wait", "--for=delete", "pod", "-l", render.LabelWorkload, "--timeout=100s")
		helm(t, "uninstall", releaseName, "-n", ns, "--ignore-not-found", "--wait", "--timeout=2m")
	})
	return r
}

// upgrade upgrades the release with sets added to the previous ones.
func (r *release) upgrade(t *testing.T, sets ...string) {
	t.Helper()
	r.sets = append(r.sets, sets...)
	r.helm(t, "upgrade")
}

func (r *release) helm(t *testing.T, verb string) {
	t.Helper()
	args := []string{verb, releaseName, filepath.Join(repoRoot(t), "charts", "continuum"), "-n", r.ns, "--timeout=5m"}
	for _, s := range r.sets {
		args = append(args, "--set-json", setJSON(s))
	}
	helm(t, args...)
}

// setJSON turns key=value into a --set-json argument: a value that is
// already JSON (a number, a bool, a list) stays, anything else is quoted.
func setJSON(kv string) string {
	k, v, _ := strings.Cut(kv, "=")
	switch {
	case v == "true", v == "false", strings.HasPrefix(v, "["), strings.HasPrefix(v, "{"), isNumber(v):
		return kv
	}
	return fmt.Sprintf("%s=%q", k, v)
}

func isNumber(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && c != '.' {
			return false
		}
	}
	return true
}
