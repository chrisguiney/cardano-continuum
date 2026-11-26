package bisect

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

// Runner runs one external command with an argument vector and returns its
// standard output. devbuild.Runner satisfies it.
type Runner interface {
	Run(ctx context.Context, dir string, env []string, name string, args ...string) (string, error)
}

// maxCommits bounds the range one bisect walks, so a mistyped range cannot
// ask for the whole history of a dependency.
const maxCommits = 5000

// refRe is the allowlist for a revision: no leading dash, no whitespace or
// shell syntax. git rev-list reads an argument starting with a dash as an
// option.
var refRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._/@^~+-]*$`)

// ParseRange splits "good..bad".
func ParseRange(s string) (good, bad string, err error) {
	good, bad, ok := strings.Cut(s, "..")
	if !ok || good == "" || bad == "" || strings.HasPrefix(bad, ".") {
		return "", "", fmt.Errorf("want <good>..<bad>, got %q", s)
	}
	return good, bad, nil
}

// ListCommits returns the commits reachable from bad and not from good,
// oldest first. It only reads the repository.
func ListCommits(ctx context.Context, run Runner, repo, good, bad string) ([]string, error) {
	for _, r := range []string{good, bad} {
		if !refRe.MatchString(r) {
			return nil, fmt.Errorf("revision %q is not a plain ref or commit", r)
		}
	}
	out, err := run.Run(ctx, repo, nil, "git", "rev-list", "--reverse", good+".."+bad)
	if err != nil {
		return nil, fmt.Errorf("list %s..%s in %s: %w", good, bad, repo, err)
	}
	commits := strings.Fields(out)
	if len(commits) > maxCommits {
		return nil, fmt.Errorf("%s..%s holds %d commits, over the %d limit", good, bad, len(commits), maxCommits)
	}
	return commits, nil
}
