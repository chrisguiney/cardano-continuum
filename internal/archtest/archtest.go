// Package archtest enforces package boundaries over the source tree. It reads
// imports only, so it runs without building anything.
package archtest

import (
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/chrisguiney/cardano-continuum/internal/ident"
)

// graph maps a package directory, relative to the module root and slash
// separated, to the module-internal packages it imports.
type graph map[string][]string

// leaves are packages that must not import any other package in this module.
// They are the bottom of the dependency graph, so an import from them would
// create a cycle as soon as the imported package imports them back.
var leaves = map[string]bool{
	"internal/ident": true,
	"internal/check": true,
}

// violations returns one message per broken rule, sorted by package. The rules:
//   - a leaf package imports no module-internal package;
//   - no package imports anything under cmd/, which holds binaries only.
func violations(g graph) []string {
	pkgs := make([]string, 0, len(g))
	for p := range g {
		pkgs = append(pkgs, p)
	}
	sort.Strings(pkgs)

	var out []string
	for _, pkg := range pkgs {
		for _, imp := range g[pkg] {
			if leaves[pkg] {
				out = append(out, fmt.Sprintf("%s is a leaf package and imports %s", pkg, imp))
			}
			if strings.HasPrefix(imp, "cmd/") {
				out = append(out, fmt.Sprintf("%s imports binary package %s", pkg, imp))
			}
		}
	}
	return out
}

// load parses the import clauses of every .go file under root. Hidden
// directories, testdata and vendor trees are skipped, so other checkouts
// under .worktrees are not read.
func load(root string) (graph, error) {
	g := graph{}
	prefix := ident.Module + "/"
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if path != root && (strings.HasPrefix(name, ".") || name == "testdata" || name == "vendor") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, filepath.Dir(path))
		if err != nil {
			return err
		}
		pkg := filepath.ToSlash(rel)
		for _, spec := range f.Imports {
			p, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				return err
			}
			if rest, ok := strings.CutPrefix(p, prefix); ok {
				g[pkg] = append(g[pkg], rest)
			}
		}
		return nil
	})
	return g, err
}
