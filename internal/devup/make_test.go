package devup

import (
	"os/exec"
	"strings"
	"testing"
)

// TestMakeDryRun pins the shape of `make -n up` and `make -n down`: the
// variables reach the scripts, no distribution's context is assumed, and
// nothing runs.
func TestMakeDryRun(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("make is not installed")
	}
	run := func(args ...string) string {
		cmd := exec.CommandContext(t.Context(), "make", append([]string{"-n", "-C", "../.."}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("make %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	up := run("up", "POOLS=5", "DINGO_POOLS=2", "NAMESPACE=lab", "CONTINUUM_REGISTRY=registry.example/c")
	for _, want := range []string{"CONTEXT=''", "POOLS='5'", "DINGO_POOLS='2'", "NAMESPACE='lab'", "WALLETS='100'", "SCALE='200'",
		"PROFILES='preprod'", "CONTINUUM_REGISTRY='registry.example/c'", "HELM='go run helm.sh/helm/v4/cmd/helm@", "ko@", "build --base-import-paths", "./cmd/continuum-ui", "-buildvcs=false", "hack/up.sh"} {
		if !strings.Contains(up, want) {
			t.Errorf("make -n up lacks %q:\n%s", want, up)
		}
	}
	if got := run("up", "SCALE=1", "CONTEXT=lab-cluster"); !strings.Contains(got, "SCALE='1'") || !strings.Contains(got, "CONTEXT='lab-cluster'") {
		t.Errorf("make -n up SCALE=1 CONTEXT=lab-cluster does not pass them:\n%s", got)
	}
	if got := run("up", "HARD_FORK=none"); !strings.Contains(got, "HARD_FORK='none'") {
		t.Errorf("make -n up HARD_FORK=none does not pass it:\n%s", got)
	}

	if down := run("down"); !strings.Contains(down, "hack/down.sh") || strings.Contains(down, "ko@") {
		t.Errorf("make -n down = %q, want hack/down.sh and no image build", down)
	}
}
