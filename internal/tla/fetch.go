package tla

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Fetch makes the model repository available at a pinned commit and returns
// its directory and the resolved commit. With a ref it clones repo (a URL or
// a path) into cache and checks the ref out detached, so the model never lands
// in the working repository and a rerun at the same ref reads the same text.
// With no ref, repo must be a directory that already holds specs/ and is read
// in place; the commit is then empty.
func Fetch(ctx context.Context, repo, ref, cache string) (dir, commit string, err error) {
	if ref == "" {
		if _, err := os.Stat(filepath.Join(repo, "specs")); err != nil {
			return "", "", fmt.Errorf("no --ref given and %s has no specs/ directory: %w", repo, err)
		}
		return repo, "", nil
	}
	if strings.HasPrefix(ref, "-") || strings.HasPrefix(repo, "-") {
		return "", "", fmt.Errorf("refusing option-like repo or ref %q %q", repo, ref)
	}
	if cache == "" {
		return "", "", errors.New("fetch needs a cache directory")
	}
	sum := sha256.Sum256([]byte(repo))
	dir = filepath.Join(cache, hex.EncodeToString(sum[:6]))
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		if err := os.MkdirAll(cache, 0o755); err != nil {
			return "", "", fmt.Errorf("create cache %s: %w", cache, err)
		}
		if err := git(ctx, "", "clone", "--quiet", "--no-checkout", "--", repo, dir); err != nil {
			return "", "", err
		}
	}
	if err := git(ctx, dir, "checkout", "--quiet", "--detach", ref+"^{commit}"); err != nil {
		if ferr := git(ctx, dir, "fetch", "--quiet", "origin", ref); ferr != nil {
			return "", "", fmt.Errorf("ref %s not found in %s: %w", ref, repo, err)
		}
		if err := git(ctx, dir, "checkout", "--quiet", "--detach", "FETCH_HEAD"); err != nil {
			return "", "", err
		}
	}
	out, err := gitOut(ctx, dir, "rev-parse", "HEAD")
	if err != nil {
		return "", "", err
	}
	return dir, strings.TrimSpace(out), nil
}

func git(ctx context.Context, dir string, args ...string) error {
	_, err := gitOut(ctx, dir, args...)
	return err
}

func gitOut(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}
