package tla

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func readSeed(t *testing.T, path string) Seed {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var s Seed
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestHarvestHello(t *testing.T) {
	t.Parallel()
	tools := testTools(t)
	models, err := filepath.Abs("../../models")
	if err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	sum, err := Harvest(context.Background(), Options{
		Repo: models, Out: out, Families: []string{"hello"}, Tools: tools,
		Timeout: time.Minute, Workers: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if sum.Violations != 1 || sum.Broken != 0 || sum.Total != 1 {
		t.Fatalf("summary = %+v", sum)
	}
	seed := readSeed(t, filepath.Join(out, "hello", "HelloBug.seed.json"))
	if seed.Status != Violation || seed.Prov.Invariant != "NeverDoneAfterPong" ||
		seed.Prov.Cfg != "HelloBug.cfg" || seed.Prov.Spec != "Hello.tla" || seed.Prov.TLC == "" {
		t.Fatalf("seed = %+v", seed.Prov)
	}
	if seed.Trace == nil || len(seed.Trace.Steps) != 4 {
		t.Fatalf("trace = %+v", seed.Trace)
	}
	// Hello.cfg is expected to pass, so it yields no seed.
	if _, err := os.Stat(filepath.Join(out, "hello", "Hello.seed.json")); !os.IsNotExist(err) {
		t.Fatalf("Hello.cfg produced a seed: %v", err)
	}
}

func copyHello(t *testing.T, repo string) string {
	t.Helper()
	dir := filepath.Join(repo, "specs", "hello")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	tla, err := os.ReadFile(filepath.Join(helloDir(t), "Hello.tla"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Hello.tla"), tla, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestHarvestBrokenCfg(t *testing.T) {
	t.Parallel()
	tools := testTools(t)
	repo := t.TempDir()
	dir := copyHello(t, repo)
	cfg := "SPECIFICATION Spec\nINVARIANT NoSuchInvariant\n"
	if err := os.WriteFile(filepath.Join(dir, "HelloBrokenBug.cfg"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	sum, err := Harvest(context.Background(), Options{
		Repo: repo, Out: out, Tools: tools, Timeout: time.Minute, Workers: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if sum.Broken != 1 || sum.Violations != 0 {
		t.Fatalf("summary = %+v", sum)
	}
	seed := readSeed(t, filepath.Join(out, "hello", "HelloBrokenBug.seed.json"))
	if seed.Status != Broken || seed.Trace != nil || seed.Detail == "" {
		t.Fatalf("seed = %+v", seed)
	}
}

func TestHarvestPinnedCommit(t *testing.T) {
	t.Parallel()
	tools := testTools(t)
	repo := t.TempDir()
	dir := copyHello(t, repo)
	good := "SPECIFICATION Spec\nINVARIANT NeverDoneAfterPong\n"
	if err := os.WriteFile(filepath.Join(dir, "HelloBug.cfg"), []byte(good), 0o644); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo, "-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"}, args...)...)
		b, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, b)
		}
		return string(b)
	}
	git("init", "-q")
	git("add", ".")
	git("commit", "-q", "-m", "first")
	first := git("rev-parse", "HEAD")
	first = first[:len(first)-1]
	if err := os.WriteFile(filepath.Join(dir, "HelloBug.cfg"), []byte("SPECIFICATION Spec\nINVARIANT Nope\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("commit", "-q", "-am", "second")

	out := t.TempDir()
	sum, err := Harvest(context.Background(), Options{
		Repo: repo, Ref: first, CacheDir: t.TempDir(), Out: out, Tools: tools,
		Timeout: time.Minute, Workers: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if sum.Commit != first || sum.Violations != 1 {
		t.Fatalf("summary = %+v, want commit %s", sum, first)
	}
	seed := readSeed(t, filepath.Join(out, "hello", "HelloBug.seed.json"))
	if seed.Prov.Commit != first || seed.Prov.Repo != repo {
		t.Fatalf("prov = %+v", seed.Prov)
	}
}

func TestSpecFor(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("Utxo.tla", "")
	write("UtxoVectors.tla", "")
	write("X.tla", "")
	write("UtxoGoBug.cfg", "SPECIFICATION Spec\n")
	write("UtxoVectorsBug.cfg", "SPECIFICATION Spec\n")
	write("OtherBug.cfg", "\\* SPEC: X.tla\nSPECIFICATION Spec\n")
	for cfg, want := range map[string]string{
		"UtxoGoBug.cfg":      "Utxo.tla",
		"UtxoVectorsBug.cfg": "UtxoVectors.tla",
		"OtherBug.cfg":       "X.tla",
	} {
		got, err := specFor(dir, cfg)
		if err != nil || got != want {
			t.Errorf("specFor(%s) = %q, %v; want %q", cfg, got, err, want)
		}
	}
	write("ZBug.cfg", "SPECIFICATION Spec\n")
	if _, err := specFor(dir, "ZBug.cfg"); err == nil {
		t.Error("want error when no module matches")
	}
}
