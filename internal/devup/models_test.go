package devup

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// modelsRepo is a throwaway git repository holding a copy of
// models/scripts/changed-models.sh and three specs, committed as the base.
func modelsRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}

	script, err := os.ReadFile("../../models/scripts/changed-models.sh")
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	files := map[string]string{
		"models/scripts/changed-models.sh": string(script),
		"models/scripts/run-model.sh":      "#!/bin/sh\n",
		"models/specs/README.md":           "specs\n",
		"models/specs/a/A.tla":             "a\n",
		"models/specs/b/B.tla":             "b\n",
		"models/specs/c/C.tla":             "c\n",
		"README.md":                        "top\n",
		".gitignore":                       "/models/specs/*/states/\n",
	}
	for name, body := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	for _, args := range [][]string{
		{"init", "-q"},
		{"add", "."},
		{"-c", "user.name=t", "-c", "user.email=t@example.invalid", "-c", "commit.gpgsign=false", "commit", "-q", "-m", "base"},
		{"tag", "base"},
	} {
		gitIn(t, dir, args...)
	}

	return dir
}

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git", args...)
	cmd.Dir = dir
	// The developer's own git config (signing, tag defaults, hooks) must
	// not reach the throwaway repository.
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// selected runs the script from inside the repository's models directory,
// as make models-changed does, and returns the spec directories it prints.
func selected(t *testing.T, dir string) []string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "scripts/changed-models.sh", "base")
	cmd.Dir = filepath.Join(dir, "models")
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("changed-models.sh: %v", err)
	}

	return strings.Fields(string(out))
}

// make models-changed checks only the specs a change touched, every spec
// when the runner or the tools changed, and none when no model changed.
func TestChangedModels(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		edit func(t *testing.T, dir string)
		want []string
	}{
		{"nothing changed", func(*testing.T, string) {}, nil},
		{"outside models", func(t *testing.T, dir string) { write(t, dir, "README.md", "edited\n") }, nil},
		{"specs README only", func(t *testing.T, dir string) { write(t, dir, "models/specs/README.md", "edited\n") }, nil},
		{"one spec edited", func(t *testing.T, dir string) { write(t, dir, "models/specs/b/B.tla", "edited\n") }, []string{"specs/b"}},
		{"new config, untracked", func(t *testing.T, dir string) { write(t, dir, "models/specs/c/CBug.cfg", "new\n") }, []string{"specs/c"}},
		{"committed change", func(t *testing.T, dir string) {
			write(t, dir, "models/specs/a/A.tla", "edited\n")
			gitIn(t, dir, "-c", "user.name=t", "-c", "user.email=t@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qam", "edit a")
		}, []string{"specs/a"}},
		{"spec deleted", func(t *testing.T, dir string) { gitIn(t, dir, "rm", "-rq", "models/specs/c") }, nil},
		{"runner changed", func(t *testing.T, dir string) { write(t, dir, "models/scripts/run-model.sh", "#!/bin/sh\necho\n") }, []string{"specs/a", "specs/b", "specs/c"}},
		{"tool pin changed", func(t *testing.T, dir string) { write(t, dir, "models/scripts/install-tla.sh", "#!/bin/sh\n") }, []string{"specs/a", "specs/b", "specs/c"}},
		{"tool wrapper changed", func(t *testing.T, dir string) { write(t, dir, "models/bin/tlc", "#!/bin/sh\n") }, []string{"specs/a", "specs/b", "specs/c"}},
		{"TLC left its working directory", func(t *testing.T, dir string) { write(t, dir, "models/specs/a/states/x.st", "x\n") }, nil},
		{"selector edited", func(t *testing.T, dir string) { write(t, dir, "models/scripts/README.md", "notes\n") }, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := modelsRepo(t)
			tc.edit(t, dir)

			if got := selected(t, dir); !slices.Equal(got, tc.want) {
				t.Errorf("selected %v, want %v", got, tc.want)
			}
		})
	}
}

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
