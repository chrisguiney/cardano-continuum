package devbuild

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// StageFast writes a go.work into dir that joins the dingo worktree and every
// overlay path, and returns its path. dir must be outside every source tree:
// the sources are only read, never written, so a build cannot dirty them.
func (b *Builder) StageFast(ctx context.Context, r Request, dir string) (string, error) {
	if err := r.validate(); err != nil {
		return "", err
	}
	if r.Mode != Fast {
		return "", fmt.Errorf("stage fast: request is %s mode", r.Mode)
	}
	uses := []string{r.Dingo}
	for _, o := range r.With {
		uses = append(uses, o.Path)
	}
	abs := make([]string, len(uses))
	for i, u := range uses {
		a, err := absOutside(u, dir)
		if err != nil {
			return "", err
		}
		abs[i] = a
	}
	if _, err := b.run.Run(ctx, dir, nil, "go", append([]string{"work", "init"}, abs...)...); err != nil {
		return "", fmt.Errorf("stage go.work: %w", err)
	}
	return filepath.Join(dir, "go.work"), nil
}

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

// StageRepro clones dingo at its commit into dir/dingo, pins each overlay in
// go.mod and runs go mod tidy. An upstream overlay is pinned with go get
// <module>@<sha>; a fork replaces the module with the fork's pseudo-version
// for that commit. It refuses an overlay checkout with uncommitted changes.
func (b *Builder) StageRepro(ctx context.Context, r Request, dir string) error {
	if err := r.validate(); err != nil {
		return err
	}
	if r.Mode != Repro {
		return fmt.Errorf("stage repro: request is %s mode", r.Mode)
	}
	revs, _, _, err := b.revisions(ctx, r)
	if err != nil {
		return err
	}
	src := filepath.Join(dir, "dingo")
	if _, err := b.run.Run(ctx, dir, nil, "git", "clone", "--quiet", "--no-checkout", r.Dingo, src); err != nil {
		return fmt.Errorf("clone dingo: %w", err)
	}
	if _, err := b.run.Run(ctx, src, nil, "git", "checkout", "--quiet", "--detach", revs[Dingo]); err != nil {
		return fmt.Errorf("check out dingo %s: %w", short(revs[Dingo], 7), err)
	}
	for _, o := range r.With {
		if err := b.pin(ctx, src, o, revs[o.Module]); err != nil {
			return err
		}
	}
	if _, err := b.run.Run(ctx, src, nil, "go", "mod", "tidy"); err != nil {
		return fmt.Errorf("go mod tidy: %w", err)
	}
	mod, err := os.ReadFile(filepath.Join(src, "go.mod"))
	if err != nil {
		return fmt.Errorf("read staged go.mod: %w", err)
	}
	if lr := localReplaces(mod); len(lr) > 0 {
		return fmt.Errorf("staged go.mod replaces modules with local paths: %s", strings.Join(lr, ", "))
	}
	return nil
}

func (b *Builder) pin(ctx context.Context, src string, o Overlay, sha string) error {
	mod, _ := modulePath(o.Module)
	if o.Fork == "" {
		if _, err := b.run.Run(ctx, src, nil, "go", "get", mod+"@"+sha); err != nil {
			return fmt.Errorf("pin %s at %s: %w", o.Module, short(sha, 7), err)
		}
		return nil
	}
	out, err := b.run.Run(ctx, src, nil, "go", "list", "-m", "-json", o.Fork+"@"+sha)
	if err != nil {
		return fmt.Errorf("resolve fork %s at %s: %w", o.Fork, short(sha, 7), err)
	}
	var m struct{ Version string }
	if err := json.Unmarshal([]byte(out), &m); err != nil || m.Version == "" {
		return fmt.Errorf("resolve fork %s at %s: no version in %q", o.Fork, short(sha, 7), out)
	}
	if _, err := b.run.Run(ctx, src, nil, "go", "mod", "edit", "-replace="+mod+"="+o.Fork+"@"+m.Version); err != nil {
		return fmt.Errorf("replace %s with fork: %w", o.Module, err)
	}
	return nil
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
