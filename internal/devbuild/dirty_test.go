package devbuild

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// gitRepo makes a repository with one commit holding file a.txt.
func gitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write(t, dir, "a.txt", "one\n")
	git(t, dir, "init", "-q", "-b", "main")
	git(t, dir, "add", ".")
	git(t, dir, "-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false", "commit", "-q", "-m", "init")
	return dir
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDirtyDetection(t *testing.T) {
	t.Parallel()
	dir := gitRepo(t)
	ctx := t.Context()
	dirty, _, err := DirtyState(ctx, dir)
	if err != nil || dirty {
		t.Fatalf("clean tree: dirty=%v err=%v", dirty, err)
	}

	write(t, dir, "a.txt", "two\n")
	dirty, h1, err := DirtyState(ctx, dir)
	if err != nil || !dirty || h1 == "" {
		t.Fatalf("modified tracked file: dirty=%v hash=%q err=%v", dirty, h1, err)
	}
	if _, h2, _ := DirtyState(ctx, dir); h2 != h1 {
		t.Fatalf("same diff twice: %q != %q", h2, h1)
	}

	write(t, dir, "new.txt", "x\n")
	_, h3, err := DirtyState(ctx, dir)
	if err != nil || h3 == h1 {
		t.Fatalf("untracked file must change the hash: %q vs %q (err %v)", h3, h1, err)
	}
	write(t, dir, "new.txt", "y\n")
	if _, h4, _ := DirtyState(ctx, dir); h4 == h3 {
		t.Fatal("untracked content change must change the hash")
	}
}

func TestDirtyStateNotARepo(t *testing.T) {
	t.Parallel()
	if _, _, err := DirtyState(t.Context(), t.TempDir()); err == nil {
		t.Fatal("want error outside a git repository")
	}
}
