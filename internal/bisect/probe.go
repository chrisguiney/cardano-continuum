package bisect

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strconv"
)

// Builder builds one commit in reproducible mode and returns an image
// reference. A build error makes the commit a Skip.
type Builder interface {
	Build(ctx context.Context, commit string) (image string, err error)
}

// Checker judges a built image.
type Checker interface {
	Check(ctx context.Context, image, commit string) (Verdict, error)
}

// Prober is a Test that builds a commit and checks the image.
type Prober struct {
	Builder Builder
	Checker Checker
}

// Test implements Test. A failed build is a Skip, unless the context ended,
// which is an error.
func (p Prober) Test(ctx context.Context, commit string) (Verdict, error) {
	img, err := p.Builder.Build(ctx, commit)
	if err != nil {
		if ctx.Err() != nil {
			return Skip, ctx.Err()
		}
		return Skip, nil
	}
	return p.Checker.Check(ctx, img, commit)
}

// FindingSource runs the simulation against image with the check's seed and
// returns the kinds of the findings it raised.
type FindingSource func(ctx context.Context, image, commit string) ([]string, error)

// KindCheck is Bad when a finding of Kind is raised and Good when not.
type KindCheck struct {
	Kind   string
	Source FindingSource
}

// Check implements Checker.
func (k KindCheck) Check(ctx context.Context, image, commit string) (Verdict, error) {
	kinds, err := k.Source(ctx, image, commit)
	if err != nil {
		return Skip, fmt.Errorf("run for finding %s: %w", k.Kind, err)
	}
	if slices.Contains(kinds, k.Kind) {
		return Bad, nil
	}
	return Good, nil
}

// skipExit is the exit status git bisect run uses for "cannot test".
const skipExit = 125

// CommandCheck runs Argv with the image under CONTINUUM_IMAGE, the commit
// under CONTINUUM_COMMIT and the seed under CONTINUUM_SEED. Exit 0 is Good,
// 125 is Skip and any other exit status is Bad, as in git bisect run. A
// command that cannot start is an error, not a verdict.
type CommandCheck struct {
	Argv []string
	Seed uint64
}

// Check implements Checker.
func (c CommandCheck) Check(ctx context.Context, image, commit string) (Verdict, error) {
	if len(c.Argv) == 0 {
		return Skip, errors.New("empty check command")
	}
	cmd := exec.CommandContext(ctx, c.Argv[0], c.Argv[1:]...)
	cmd.Env = append(os.Environ(), "CONTINUUM_IMAGE="+image, "CONTINUUM_COMMIT="+commit, "CONTINUUM_SEED="+strconv.FormatUint(c.Seed, 10))
	err := cmd.Run()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return Good, nil
	case ctx.Err() != nil:
		return Skip, ctx.Err()
	case errors.As(err, &exit) && exit.ExitCode() == skipExit:
		return Skip, nil
	case errors.As(err, &exit):
		return Bad, nil
	}
	return Skip, fmt.Errorf("run %s: %w", c.Argv[0], err)
}
