package bisect

import (
	"fmt"
	"os/exec"
	"strings"

	"context"
	"errors"
	"github.com/chrisguiney/cardano-continuum/internal/devbuild"
	"reflect"
	"testing"
)

type fakeGit struct {
	dir  string
	args []string
	out  string
	err  error
}

func (g *fakeGit) Run(_ context.Context, dir string, _ []string, name string, args ...string) (string, error) {
	g.dir, g.args = dir, append([]string{name}, args...)
	return g.out, g.err
}

func TestListCommitsOldestFirst(t *testing.T) {
	g := &fakeGit{out: "aaa\nbbb\nccc\n"}
	got, err := ListCommits(context.Background(), g, "/repo", "v1", "main")
	if err != nil || !reflect.DeepEqual(got, []string{"aaa", "bbb", "ccc"}) {
		t.Fatalf("got %v, %v", got, err)
	}
	want := []string{"git", "rev-list", "--reverse", "v1..main"}
	if g.dir != "/repo" || !reflect.DeepEqual(g.args, want) {
		t.Fatalf("ran %v in %s", g.args, g.dir)
	}
}

func TestListCommitsRefusesOptionLookingRefs(t *testing.T) {
	for _, r := range []string{"--output=x", "", "a b", "a;b", "$(x)"} {
		if _, err := ListCommits(context.Background(), &fakeGit{}, "/repo", r, "main"); err == nil {
			t.Errorf("good %q: want error", r)
		}
		if _, err := ListCommits(context.Background(), &fakeGit{}, "/repo", "v1", r); err == nil {
			t.Errorf("bad %q: want error", r)
		}
	}
}

func TestListCommitsGitFailure(t *testing.T) {
	boom := errors.New("bad revision")
	if _, err := ListCommits(context.Background(), &fakeGit{err: boom}, "/repo", "v1", "main"); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
}

func TestParseRange(t *testing.T) {
	g, b, err := ParseRange("v1.0..main")
	if err != nil || g != "v1.0" || b != "main" {
		t.Fatalf("%q %q %v", g, b, err)
	}
	for _, in := range []string{"", "a", "..b", "a..", "a...b"} {
		if _, _, err := ParseRange(in); err == nil {
			t.Errorf("%q: want error", in)
		}
	}
}

func TestListCommitsOnARealRepository(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q")
	var shas []string
	for i := 0; i < 3; i++ {
		git("commit", "-q", "--allow-empty", "-m", fmt.Sprint("c", i))
		shas = append(shas, git("rev-parse", "HEAD"))
	}
	got, err := ListCommits(context.Background(), devbuild.ExecRunner{}, dir, shas[0], shas[2])
	if err != nil || !reflect.DeepEqual(got, shas[1:]) {
		t.Fatalf("got %v, %v; want %v", got, err, shas[1:])
	}
}
