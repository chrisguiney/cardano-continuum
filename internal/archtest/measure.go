package archtest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
)

// Limits from the development guidelines.
const (
	maxFiles   = 25 // files per package, test files counted
	maxMembers = 20 // fields plus methods per type
	maxFunc    = 80 // lines per function
	maxNest    = 3  // control-flow levels per function
)

// fnStat is one top-level function or method.
type fnStat struct {
	pkg, name   string
	lines, nest int
}

// typeStat is one declared type with the methods that name it as receiver.
type typeStat struct {
	pkg, name, file string
	fields          int
	methods         int
	stray           int // methods declared outside the type's file
}

// stats is everything the limit rules read from the tree.
type stats struct {
	funcs map[string]*fnStat // key "pkg name"
	types map[string]*typeStat
	files map[string]int // package directory to file count, tests included
}

// finding is one limit break. key names it without a line number so a
// finding survives unrelated edits; n is the measured size.
type finding struct {
	key string
	n   int
}

// nester finds the deepest nesting under a node.
type nester struct{ deepest int }

// nests reports whether n opens a new nesting level.
func nests(n ast.Node) bool {
	switch n.(type) {
	case *ast.IfStmt, *ast.ForStmt, *ast.RangeStmt, *ast.SwitchStmt,
		*ast.TypeSwitchStmt, *ast.SelectStmt, *ast.FuncLit:
		return true
	}
	return false
}

// walk visits the nesting levels directly under n, which sits at level d.
func (v *nester) walk(n ast.Node, d int) {
	ast.Inspect(n, func(c ast.Node) bool {
		if c == n || !nests(c) {
			return true
		}
		v.deepest = max(v.deepest, d+1)
		v.walk(c, d+1)
		return false
	})
}

// depth returns the deepest nesting of control-flow statements and function
// literals inside n.
func depth(n ast.Node) int {
	var v nester
	v.walk(n, 0)
	return v.deepest
}

// recvName returns the receiver's type name, or "" for a shape it does not
// know.
func recvName(fd *ast.FuncDecl) string {
	t := fd.Recv.List[0].Type
	if s, ok := t.(*ast.StarExpr); ok {
		t = s.X
	}
	switch x := t.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.IndexExpr:
		if id, ok := x.X.(*ast.Ident); ok {
			return id.Name
		}
	case *ast.IndexListExpr:
		if id, ok := x.X.(*ast.Ident); ok {
			return id.Name
		}
	}
	return ""
}

// fieldCount counts the fields of a struct, an embedded field counting one.
func fieldCount(st *ast.StructType) int {
	n := 0
	for _, f := range st.Fields.List {
		n += max(1, len(f.Names))
	}
	return n
}

// scanner accumulates stats over the files of one walk.
type scanner struct {
	fset *token.FileSet
	st   *stats
}

// methodRec is a method seen before its receiver type may have been.
type methodRec struct{ pkg, typ, file string }

// scan measures every Go file under root. Types are keyed by directory and
// name; a method counts toward the type whatever file it is in, but only a
// non-test, non-generated file counts as stray.
func scan(root string) (*stats, error) {
	s := &scanner{
		fset: token.NewFileSet(),
		st: &stats{
			funcs: map[string]*fnStat{},
			types: map[string]*typeStat{},
			files: map[string]int{},
		},
	}
	var methods []methodRec
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skipDir(root, path, d.Name()) || d.Name() == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		ms, err := s.file(filepath.ToSlash(rel), path)
		methods = append(methods, ms...)
		return err
	})
	if err != nil {
		return nil, err
	}
	for _, m := range methods {
		t := s.st.types[m.pkg+" "+m.typ]
		if t == nil {
			continue
		}
		t.methods++
		if m.file != t.file && !strings.HasSuffix(m.file, "_test.go") && !strings.HasPrefix(filepath.Base(m.file), "zz_generated") {
			t.stray++
		}
	}
	return s.st, nil
}

// file records one file's types and functions and returns its methods.
func (s *scanner) file(rel, path string) ([]methodRec, error) {
	f, err := parser.ParseFile(s.fset, path, nil, 0)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", rel, err)
	}
	pkg := filepath.ToSlash(filepath.Dir(rel))
	s.st.files[pkg]++
	test := strings.HasSuffix(rel, "_test.go")
	var methods []methodRec
	for _, d := range f.Decls {
		switch x := d.(type) {
		case *ast.GenDecl:
			s.types(pkg, rel, x)
		case *ast.FuncDecl:
			if m, ok := s.fn(pkg, rel, x, test); ok {
				methods = append(methods, m)
			}
		}
	}
	return methods, nil
}

// fn records one function and returns its receiver record when it is a method.
func (s *scanner) fn(pkg, rel string, x *ast.FuncDecl, test bool) (methodRec, bool) {
	name := x.Name.Name
	var m methodRec
	if x.Recv != nil {
		r := recvName(x)
		if r == "" {
			return m, false
		}
		m = methodRec{pkg, r, rel}
		name = r + "." + name
	}
	if !test && x.Body != nil {
		lines := s.fset.Position(x.End()).Line - s.fset.Position(x.Pos()).Line + 1
		s.st.funcs[pkg+" "+name] = &fnStat{pkg: pkg, name: name, lines: lines, nest: depth(x.Body)}
	}
	return m, x.Recv != nil
}

// types records the type declarations of one GenDecl.
func (s *scanner) types(pkg, rel string, x *ast.GenDecl) {
	if x.Tok != token.TYPE {
		return
	}
	for _, sp := range x.Specs {
		ts := sp.(*ast.TypeSpec)
		t := &typeStat{pkg: pkg, name: ts.Name.Name, file: rel}
		if st, ok := ts.Type.(*ast.StructType); ok {
			t.fields = fieldCount(st)
		}
		s.st.types[pkg+" "+ts.Name.Name] = t
	}
}

// limits returns every limit break in m, sorted by key.
func limits(m *stats) []finding {
	var out []finding
	for _, f := range m.funcs {
		if f.lines > maxFunc {
			out = append(out, finding{fmt.Sprintf("func-length %s %s", f.pkg, f.name), f.lines})
		}
		if f.nest > maxNest {
			out = append(out, finding{fmt.Sprintf("nesting %s %s", f.pkg, f.name), f.nest})
		}
	}
	for _, t := range m.types {
		if n := t.fields + t.methods; n > maxMembers {
			out = append(out, finding{fmt.Sprintf("members %s %s", t.pkg, t.name), n})
		}
		if t.stray > 0 {
			out = append(out, finding{fmt.Sprintf("stray-methods %s %s", t.pkg, t.name), t.stray})
		}
	}
	for pkg, n := range m.files {
		if n > maxFiles {
			out = append(out, finding{fmt.Sprintf("files %s", pkg), n})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].key < out[j].key })
	return out
}
