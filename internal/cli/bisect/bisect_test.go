package bisect

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	ibisect "github.com/chrisguiney/cardano-continuum/internal/bisect"
	"github.com/chrisguiney/cardano-continuum/internal/devbuild"
)

func TestParseBisect(t *testing.T) {
	t.Parallel()
	got, err := parseBisect([]string{"--dep", "gouroboros", "v1..main", "--seed", "9", "--check", "sh -c true", "--repo", "/g", "--dingo", "/d"})
	if err != nil {
		t.Fatal(err)
	}
	if got.dep != "gouroboros" || got.good != "v1" || got.bad != "main" || got.seed != 9 ||
		got.repo != "/g" || got.dingo != "/d" || strings.Join(got.argv, " ") != "sh -c true" || got.kind != "" {
		t.Fatalf("got %+v", got)
	}
}

func TestParseBisectKindCheck(t *testing.T) {
	t.Parallel()
	got, err := parseBisect([]string{"--dep", "dingo", "a..b", "--check", "wrong_class", "--repo", "/d"})
	if err != nil || got.kind != "wrong_class" || got.argv != nil {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestParseBisectRefusals(t *testing.T) {
	t.Parallel()
	for name, args := range map[string][]string{
		"no dep":              {"a..b", "--check", "true", "--repo", "/r"},
		"unknown dep":         {"--dep", "zlib", "a..b", "--check", "true", "--repo", "/r"},
		"no range":            {"--dep", "dingo", "--check", "true", "--repo", "/r"},
		"bad range":           {"--dep", "dingo", "ab", "--check", "true", "--repo", "/r"},
		"no check":            {"--dep", "dingo", "a..b", "--repo", "/r"},
		"no repo":             {"--dep", "dingo", "a..b", "--check", "true"},
		"overlay needs dingo": {"--dep", "plutigo", "a..b", "--check", "true", "--repo", "/r"},
	} {
		if _, err := parseBisect(args); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}

type listGit struct{ out string }

func (g listGit) Run(context.Context, string, []string, string, ...string) (string, error) {
	return g.out, nil
}

type commitBuilder struct{ fail map[string]bool }

func (b commitBuilder) Build(_ context.Context, c string) (string, error) {
	if b.fail[c] {
		return "", errors.New("build failed")
	}
	return "img-" + c, nil
}

type threshold struct{ firstBad string }

func (c threshold) Check(_ context.Context, _, commit string) (ibisect.Verdict, error) {
	if commit >= c.firstBad {
		return ibisect.Bad, nil
	}
	return ibisect.Good, nil
}

func TestRunBisectReportsTheFirstBadCommit(t *testing.T) {
	t.Parallel()
	a := bisectArgs{dep: "gouroboros", good: "g", bad: "b", repo: "/r"}
	git := listGit{"c1\nc2\nc3\nc4\nc5\n"}
	var out bytes.Buffer
	err := runBisect(context.Background(), a, git, ibisect.Prober{Builder: commitBuilder{fail: map[string]bool{"c3": true}}, Checker: threshold{"c2"}}, &out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "first bad commit: c2") {
		t.Fatalf("output: %s", out.String())
	}
}

func TestRunBisectNamesAnAmbiguousRange(t *testing.T) {
	t.Parallel()
	a := bisectArgs{dep: "gouroboros", good: "g", bad: "b", repo: "/r"}
	git := listGit{"c1\nc2\nc3\nc4\n"}
	var out bytes.Buffer
	err := runBisect(context.Background(), a, git, ibisect.Prober{Builder: commitBuilder{fail: map[string]bool{"c2": true, "c3": true}}, Checker: threshold{"c3"}}, &out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "one of") || !strings.Contains(out.String(), "c2") {
		t.Fatalf("output: %s", out.String())
	}
}

func TestDevBuilderRequestsRepro(t *testing.T) {
	t.Parallel()
	d := devBuilder{dep: "dingo", repo: "/d"}
	if r := d.request("abc"); r.Mode != devbuild.Repro || r.Dingo != "/d" || r.Ref != "abc" || len(r.With) != 0 {
		t.Fatalf("dingo request %+v", r)
	}
	o := devBuilder{dep: "plutigo", repo: "/p", dingo: "/d"}
	r := o.request("abc")
	if r.Mode != devbuild.Repro || r.Dingo != "/d" || r.Ref != "" || len(r.With) != 1 ||
		r.With[0] != (devbuild.Overlay{Module: "plutigo", Path: "/p", Sha: "abc"}) {
		t.Fatalf("overlay request %+v", r)
	}
}
