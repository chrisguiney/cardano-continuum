package devup

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// upStubs are the kubectl and helm stand-ins hack/up.sh runs under test.
// kubectl answers the engine's run annotation from DEPLOYED_RUN; helm logs
// its arguments to HELM_LOG, reports a release only when RELEASED is set,
// and returns the deployed pool values make up compares.
var upStubs = map[string]string{
	"kubectl": `#!/usr/bin/env bash
case "$*" in
  *"get statefulset continuum-engine"*) printf '%s' "${DEPLOYED_RUN:-}" ;;
esac
exit 0
`,
	"helm": `#!/usr/bin/env bash
printf '%s\n' "$*" >>"$HELM_LOG"
case " $* " in
  *" status "*) [ -n "${RELEASED:-}" ]; exit ;;
  *" get values "*) echo '{"pools":{"cardanoNode":3,"dingo":1},"genesis":{"overrides":[]}}' ;;
esac
exit 0
`,
}

// upRun sources hack/up.sh with stubbed kubectl and helm, the images file
// naming engine and ui digests, and env, then runs snippet. It returns the
// combined output and whether the snippet succeeded.
func upRun(t *testing.T, engine, ui string, env []string, snippet string) (string, bool) {
	t.Helper()
	dir := t.TempDir()
	for name, body := range upStubs {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	const reg = "registry.example/c"
	images := strings.Join([]string{
		reg + "/continuum@sha256:" + strings.Repeat(engine, 64),
		reg + "/continuum-operator@sha256:" + strings.Repeat("1", 64),
		reg + "/continuum-genesis@sha256:" + strings.Repeat("2", 64),
		reg + "/continuum-ui@sha256:" + strings.Repeat(ui, 64),
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(dir, "images"), []byte(images), 0o644); err != nil {
		t.Fatal(err)
	}

	script := `source "$UP"
ENGINE_ATTACKS=dust-flood OVERRIDES=() ALLOW_INCONSISTENT=false
` + snippet
	cmd := exec.CommandContext(t.Context(), "bash", "-c", script)
	cmd.Env = append(os.Environ(),
		"UP=../../hack/up.sh",
		"CONTEXT=test", "NAMESPACE=ns", "RELEASE=continuum", "PROFILES=preprod", "KEEP_RUN=1",
		"CONTINUUM_REGISTRY="+reg, "IMAGES="+filepath.Join(dir, "images"),
		"HELM="+filepath.Join(dir, "helm"), "K_BIN="+filepath.Join(dir, "kubectl"),
		"HELM_LOG="+filepath.Join(dir, "helm.log"), "CONTINUUM_STATE_DIR="+filepath.Join(dir, "state"),
	)
	cmd.Env = append(cmd.Env, env...)
	out, err := cmd.CombinedOutput()
	if log, rerr := os.ReadFile(filepath.Join(dir, "helm.log")); rerr == nil {
		out = append(out, log...)
	}
	return string(out), err == nil
}

// TestUpResetsOnlyForTheEngine: a deploy that changes only the UI image
// keeps the run, and one that changes the engine image resets it (refused
// here, under KEEP_RUN=1), because the run identity names the engine image
// alone. The UI image still reaches the chart.
func TestUpResetsOnlyForTheEngine(t *testing.T) {
	t.Parallel()
	for _, tool := range []string{"bash", "jq"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed", tool)
		}
	}

	out, ok := upRun(t, "a", "c", nil, `check_run && printf 'run=%s\n' "$RUN_ID"`)
	_, id, found := strings.Cut(out, "run=")
	if !ok || !found {
		t.Fatalf("first deploy:\n%s", out)
	}
	deployed := []string{"RELEASED=1", "DEPLOYED_RUN=" + strings.TrimSpace(strings.SplitN(id, "\n", 2)[0])}

	if out, ok := upRun(t, "a", "d", deployed, "check_run"); !ok || strings.Contains(out, "resetting") {
		t.Errorf("a UI-only change reset the run:\n%s", out)
	}

	if out, ok := upRun(t, "b", "c", deployed, "check_run"); ok || !strings.Contains(out, "engine binary changed") {
		t.Errorf("an engine change kept the run:\n%s", out)
	}

	out, ok = upRun(t, "a", "d", deployed, `RUN_ID=x install`)
	if !ok || !strings.Contains(out, "images.ui.digest=sha256:"+strings.Repeat("d", 64)) || !strings.Contains(out, "ui.port=8088") {
		t.Errorf("install does not pass the UI image and port to the chart:\n%s", out)
	}
}
