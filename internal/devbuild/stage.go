package devbuild

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"
)

// realPath is p made absolute with symlinks resolved. go.work lists real
// paths: on macOS the temp dir is a symlink, and go rejects a workspace
// module reached through one.
func realPath(p string) (string, error) {
	a, err := filepath.Abs(p)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", p, err)
	}
	r, err := filepath.EvalSymlinks(a)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", p, err)
	}
	return r, nil
}

// absOutside returns src's absolute path, refusing a staging dir inside it.
func absOutside(src, stage string) (string, error) {
	a, err := realPath(src)
	if err != nil {
		return "", err
	}
	s, err := realPath(stage)
	if err != nil {
		return "", err
	}
	if rel, err := filepath.Rel(a, s); err == nil && !strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("staging dir %s is inside source tree %s", stage, src)
	}
	return a, nil
}

// localReplaces lists go.mod replace directives that point at a local path.
// Comment lines do not count: dingo ships commented-out ones.
func localReplaces(gomod []byte) []string {
	var out []string
	inBlock := false
	for _, line := range bytes.Split(gomod, []byte("\n")) {
		l := strings.TrimSpace(string(line))
		if strings.HasPrefix(l, "//") {
			continue
		}
		switch {
		case l == "replace (":
			inBlock = true
			continue
		case inBlock && l == ")":
			inBlock = false
			continue
		case strings.HasPrefix(l, "replace "):
			l = strings.TrimPrefix(l, "replace ")
		case !inBlock:
			continue
		}
		_, to, ok := strings.Cut(l, "=>")
		if !ok {
			continue
		}
		to = strings.TrimSpace(to)
		if strings.HasPrefix(to, ".") || strings.HasPrefix(to, "/") {
			out = append(out, l)
		}
	}
	return out
}
