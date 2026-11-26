package devup

import (
	"os/exec"
	"strings"
	"testing"
)

// TestMakeDryRun pins the shape of `make -n up` and `make -n down`: the
// variables reach the scripts, and nothing runs.
func TestMakeDryRun(t *testing.T) {
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
	up := run("up", "POOLS=5", "DINGO_POOLS=2", "NAMESPACE=lab")
	for _, want := range []string{"CONTEXT='docker-desktop'", "POOLS='5'", "DINGO_POOLS='2'", "NAMESPACE='lab'", "WALLETS='100'", "SCALE='200'", "PROFILES='preprod'", "hack/up.sh"} {
		if !strings.Contains(up, want) {
			t.Errorf("make -n up lacks %q:\n%s", want, up)
		}
	}
	if got := run("up", "SCALE=1"); !strings.Contains(got, "SCALE='1'") {
		t.Errorf("make -n up SCALE=1 does not pass SCALE:\n%s", got)
	}
	if down := run("down"); !strings.Contains(down, "hack/down.sh") {
		t.Errorf("make -n down = %q", down)
	}
}
