package contract

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const templatesPath = "../../contracts/templates"

// readTemplates returns the Aiken sources of the project, by path
// relative to it.
func readTemplates(t *testing.T) map[string]string {
	t.Helper()
	out, err := ReadSources(os.DirFS(templatesPath))
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// sources_gen.go snapshots the .ak files; a changed source without
// make contracts fails here, without needing Aiken.
func TestSourcesMatchTemplates(t *testing.T) {
	t.Parallel()
	got, err := RenderSources(readTemplates(t))
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("sources_gen.go")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("sources_gen.go drifts from contracts/templates: run make contracts")
	}
}

// Every validator has its module in the snapshot, and the snapshot has
// every .ak file of the project.
func TestSourcesCoverTemplates(t *testing.T) {
	t.Parallel()
	var onDisk []string
	for _, dir := range []string{"lib", "validators"} {
		err := filepath.WalkDir(filepath.Join(templatesPath, dir), func(p string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() && strings.HasSuffix(p, ".ak") {
				rel, _ := filepath.Rel(templatesPath, p)
				onDisk = append(onDisk, filepath.ToSlash(rel))
			}
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	slices.Sort(onDisk)
	if !slices.Equal(onDisk, sourcePaths()) {
		t.Fatalf("snapshot %v, on disk %v", sourcePaths(), onDisk)
	}
	for _, v := range All() {
		if _, ok := sourceFiles[v.Module]; !ok {
			t.Errorf("%s: module %s not in the snapshot", v.Name, v.Module)
		}
	}
}

func TestSourcesOfFollowsImports(t *testing.T) {
	t.Parallel()
	v, _ := Named("vesting")
	got := SourcesOf(v)
	if len(got) < 2 || got[0].Path != "validators/vesting.ak" {
		t.Fatalf("sources %v", got)
	}
	if !slices.ContainsFunc(got, func(s Source) bool { return s.Path == "lib/continuum/types.ak" }) {
		t.Errorf("vesting does not list lib/continuum/types.ak: %v", got)
	}
	m, _ := Named("free_mint")
	for _, s := range SourcesOf(m) {
		if s.Path == "lib/continuum/types.ak" && !strings.Contains(sourceFiles["validators/free_mint.ak"], "continuum/types") {
			t.Errorf("free_mint lists a module it does not import")
		}
	}
}

func TestImportsParse(t *testing.T) {
	t.Parallel()
	src := "use aiken/collection/list\nuse cardano/transaction.{OutputReference, Transaction}\nuse continuum/types as t\n  use no/indent\n"
	want := []string{"aiken/collection/list", "cardano/transaction", "continuum/types"}
	if got := imports(src); !slices.Equal(got, want) {
		t.Fatalf("imports %v, want %v", got, want)
	}
}
