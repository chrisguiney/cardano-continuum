package devbuild

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/chrisguiney/cardano-continuum/internal/operator/image"
)

// hybrid runs every command for real unless fake claims it. Calls are recorded
// as space-joined command lines.
type hybrid struct {
	fake  func(dir, name string, args []string) (out string, handled bool)
	mu    sync.Mutex // protects calls
	calls []string
}

func (h *hybrid) Run(ctx context.Context, dir string, env []string, name string, args ...string) (string, error) {
	h.mu.Lock()
	h.calls = append(h.calls, name+" "+strings.Join(args, " "))
	h.mu.Unlock()
	if h.fake != nil {
		if out, ok := h.fake(dir, name, args); ok {
			return out, nil
		}
	}
	return (ExecRunner{}).Run(ctx, dir, env, name, args...)
}

func (h *hybrid) called(prefix string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, c := range h.calls {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

// fixture builds a dingo-shaped module that imports a gouroboros-shaped one,
// each in its own git repository.
func fixture(t *testing.T) (dingo, gouro string) {
	t.Helper()
	dingo = gitRepoWith(t, map[string]string{
		"go.mod":                      "module example.com/dingo\n\ngo 1.21\n\nrequire github.com/blinklabs-io/gouroboros v0.0.0\n",
		"internal/version/version.go": "package version\n\nvar Version, CommitHash string\n",
		"cmd/dingo/main.go":           "package main\n\nimport (\n\t\"fmt\"\n\n\t\"example.com/dingo/internal/version\"\n\tgouro \"github.com/blinklabs-io/gouroboros\"\n)\n\nfunc main() { fmt.Println(version.Version, version.CommitHash, gouro.Name) }\n",
		"Dockerfile":                  "FROM x AS build\nCOPY --from=build /code/dingo /bin/\n",
	})
	gouro = gitRepoWith(t, map[string]string{
		"go.mod":   "module github.com/blinklabs-io/gouroboros\n\ngo 1.21\n",
		"gouro.go": "package gouro\n\nconst Name = \"gouro\"\n",
	})
	return dingo, gouro
}

func gitRepoWith(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for n, b := range files {
		write(t, dir, n, b)
	}
	git(t, dir, "init", "-q", "-b", "main")
	git(t, dir, "add", ".")
	git(t, dir, "-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false", "commit", "-q", "-m", "init")
	return dir
}

// treeHash covers every file's path, content and mtime outside .git, and the
// porcelain status.
func treeHash(t *testing.T, root string) string {
	t.Helper()
	h := sha256.New()
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		h.Write([]byte(p + info.ModTime().String()))
		h.Write(b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	h.Write([]byte(git(t, root, "status", "--porcelain")))
	return hex.EncodeToString(h.Sum(nil))
}

func TestStageFastLeavesWorktreesUntouched(t *testing.T) {
	t.Parallel()
	dingo, gouro := fixture(t)
	write(t, gouro, "gouro.go", "package gouro\n\nconst Name = \"dirty gouro\"\n")
	before := treeHash(t, dingo) + treeHash(t, gouro)

	b := New(&hybrid{}, nil, image.Settings{}, nil)
	stage := t.TempDir()
	r := Request{Dingo: dingo, Mode: Fast, With: []Overlay{{Module: Gouroboros, Path: gouro}}}
	goWork, err := b.StageFast(t.Context(), r, stage)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(goWork) != stage {
		t.Fatalf("go.work at %s, want inside %s", goWork, stage)
	}
	raw, _ := os.ReadFile(goWork)
	for _, p := range []string{dingo, gouro} {
		if resolved, _ := filepath.EvalSymlinks(p); !strings.Contains(string(raw), p) && !strings.Contains(string(raw), resolved) {
			t.Errorf("go.work does not use %s:\n%s", p, raw)
		}
	}
	out := filepath.Join(stage, "dingo")
	if err := b.compile(t.Context(), dingo, goWork, out, runtime.GOOS, runtime.GOARCH, "v-test", "deadbeef"); err != nil {
		t.Fatal(err)
	}
	if after := treeHash(t, dingo) + treeHash(t, gouro); after != before {
		t.Fatal("staging and building changed a source worktree")
	}
	if _, err := os.Stat(filepath.Join(dingo, "go.work")); err == nil {
		t.Fatal("go.work was written into the dingo worktree")
	}

	run, err := exec.Command(out).Output()
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(run)); got != "v-test deadbeef dirty gouro" {
		t.Fatalf("binary printed %q, want the ldflags values and the dirty overlay source", got)
	}
	info, err := exec.Command("go", "version", "-m", out).Output()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(info), "-tags=dingo_extra_plugins") {
		t.Fatalf("go version -m lacks the build tag:\n%s", info)
	}
}

func TestStageFastRejectsBadOverlay(t *testing.T) {
	t.Parallel()
	dingo, gouro := fixture(t)
	b := New(&hybrid{}, nil, image.Settings{}, nil)
	for name, o := range map[string]Overlay{
		"unknown module": {Module: "nope", Path: gouro},
		"no path":        {Module: Gouroboros},
	} {
		r := Request{Dingo: dingo, Mode: Fast, With: []Overlay{o}}
		if _, err := b.StageFast(t.Context(), r, t.TempDir()); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}

func TestRevisionsRejectsShaNotAtHead(t *testing.T) {
	t.Parallel()
	dingo, gouro := fixture(t)
	b := New(&hybrid{}, nil, image.Settings{}, nil)
	r := Request{Dingo: dingo, Mode: Fast, With: []Overlay{{Module: Gouroboros, Path: gouro, Sha: "0000000"}}}
	if _, _, _, err := b.revisions(t.Context(), r); err == nil {
		t.Fatal("want error when the overlay checkout is not at the named commit")
	}
}

func TestStageReproGoGet(t *testing.T) {
	t.Parallel()
	dingo, gouro := fixture(t)
	sha := strings.TrimSpace(git(t, gouro, "rev-parse", "HEAD"))
	const pseudo = "v0.0.0-20260102030405-abcdef123456"
	const forkPseudo = "v0.0.0-20260102030405-123456abcdef"
	h := &hybrid{fake: func(dir, name string, args []string) (string, bool) {
		line := name + " " + strings.Join(args, " ")
		switch line {
		case "go get github.com/blinklabs-io/gouroboros@" + sha:
			// What go get leaves in go.mod for a commit with no tag.
			if _, err := (ExecRunner{}).Run(t.Context(), dir, nil, "go", "mod", "edit", "-require=github.com/blinklabs-io/gouroboros@"+pseudo); err != nil {
				t.Error(err)
			}
			return "", true
		case "go list -m -json github.com/fork/gouro@" + sha:
			return `{"Path":"github.com/fork/gouro","Version":"` + forkPseudo + `"}`, true
		case "go mod tidy":
			return "", true
		}
		return "", false
	}}
	b := New(h, nil, image.Settings{}, nil)

	r := Request{Dingo: dingo, Mode: Repro, With: []Overlay{{Module: Gouroboros, Path: gouro}}}
	stage := t.TempDir()
	if err := b.StageRepro(t.Context(), r, stage); err != nil {
		t.Fatal(err)
	}
	mod, _ := os.ReadFile(filepath.Join(stage, "dingo", "go.mod"))
	if !strings.Contains(string(mod), "github.com/blinklabs-io/gouroboros "+pseudo) {
		t.Fatalf("go.mod lacks the pseudo-version:\n%s", mod)
	}
	if !h.called("go mod tidy") {
		t.Fatal("go mod tidy did not run")
	}

	r.With[0].Fork = "github.com/fork/gouro"
	stage = t.TempDir()
	if err := b.StageRepro(t.Context(), r, stage); err != nil {
		t.Fatal(err)
	}
	mod, _ = os.ReadFile(filepath.Join(stage, "dingo", "go.mod"))
	if want := "replace github.com/blinklabs-io/gouroboros => github.com/fork/gouro " + forkPseudo; !strings.Contains(string(mod), want) {
		t.Fatalf("go.mod lacks %q:\n%s", want, mod)
	}
	if lr := localReplaces(mod); len(lr) != 0 {
		t.Fatalf("local replaces left: %v", lr)
	}
}

func TestStageReproRefusesDirtyOverlay(t *testing.T) {
	t.Parallel()
	dingo, gouro := fixture(t)
	write(t, gouro, "gouro.go", "package gouro\n\nconst Name = \"x\"\n")
	b := New(&hybrid{}, nil, image.Settings{}, nil)
	r := Request{Dingo: dingo, Mode: Repro, With: []Overlay{{Module: Gouroboros, Path: gouro}}}
	if err := b.StageRepro(t.Context(), r, t.TempDir()); err == nil {
		t.Fatal("repro mode must refuse uncommitted overlay source")
	}
}

func TestLocalReplaces(t *testing.T) {
	t.Parallel()
	mod := []byte("module x\n\n// replace a => ../a\nreplace b => ../b\nreplace (\n\tc => /abs/c\n\td => github.com/f/d v1.0.0\n)\n")
	got := localReplaces(mod)
	if len(got) != 2 {
		t.Fatalf("localReplaces = %v, want b and c", got)
	}
}

func discard() *slog.Logger { return slog.New(slog.DiscardHandler) }
