package devbuild

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

// DirtyState reports whether worktree has uncommitted changes, and if so a
// hash of them: the diff against HEAD plus every untracked file's path and
// content. The same changes always hash the same. Ignored files do not count,
// since they are not part of what a clone would build.
func DirtyState(ctx context.Context, worktree string) (dirty bool, diffHash string, err error) {
	status, err := gitOut(ctx, worktree, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return false, "", err
	}
	if len(bytes.TrimSpace(status)) == 0 {
		return false, "", nil
	}
	h := sha256.New()
	diff, err := gitOut(ctx, worktree, "diff", "HEAD", "--binary")
	if err != nil {
		return false, "", err
	}
	h.Write(diff)
	list, err := gitOut(ctx, worktree, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return false, "", err
	}
	names := strings.Split(strings.TrimRight(string(list), "\x00"), "\x00")
	slices.Sort(names)
	for _, n := range names {
		if n == "" {
			continue
		}
		body, err := os.ReadFile(filepath.Join(worktree, n))
		if err != nil {
			return false, "", fmt.Errorf("hash untracked %s in %s: %w", n, worktree, err)
		}
		_, _ = fmt.Fprintf(h, "\x00%s\x00%d\x00", n, len(body))
		h.Write(body)
	}
	return true, hex.EncodeToString(h.Sum(nil)), nil
}

// gitOut runs git in dir. GIT_OPTIONAL_LOCKS=0 keeps status from refreshing
// the index, so inspecting a worktree writes nothing into it.
func gitOut(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s in %s: %w: %s", args[0], dir, err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}
