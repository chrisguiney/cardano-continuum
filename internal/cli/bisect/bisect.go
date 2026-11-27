package bisect

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"runtime"
	"slices"
	"strings"

	ibisect "github.com/chrisguiney/cardano-continuum/internal/bisect"
	"github.com/chrisguiney/cardano-continuum/internal/cli/shared/flagx"
	"github.com/chrisguiney/cardano-continuum/internal/cli/shared/lines"
	"github.com/chrisguiney/cardano-continuum/internal/devbuild"
	"github.com/chrisguiney/cardano-continuum/internal/operator/image"
	"github.com/chrisguiney/cardano-continuum/internal/oracle"
)

const bisectUsage = "usage: continuum bisect --dep gouroboros|plutigo|ouroboros-mock|dingo <good>..<bad> --repo DIR --check KIND|COMMAND [--dingo DIR] [--seed N]"

var bisectDeps = []string{devbuild.Dingo, devbuild.Gouroboros, devbuild.Plutigo, devbuild.OuroborosMock}

// findingKinds are the oracle kinds a --check may name.
var findingKinds = []oracle.Kind{
	oracle.UnexpectedAccept, oracle.UnexpectedReject, oracle.WrongClass, oracle.Unclassified, oracle.VerdictSplit,
	oracle.ValueNotConserved, oracle.PaidTwice, oracle.ReservationLeaked, oracle.ReservationDouble, oracle.Unreserved,
	oracle.SettledBack, oracle.ViewMismatch, oracle.Frozen, oracle.DeepFork, oracle.FloorBreached,
}

// bisectArgs are the parsed flags of `continuum bisect`.
type bisectArgs struct {
	dep, good, bad string
	repo, dingo    string
	seed           uint64
	// kind is set when --check names an oracle finding kind; otherwise argv
	// is the command to run.
	kind string
	argv []string
	reg  string
	arch string
	// insecure is nil when --registry-insecure was not given.
	insecure *bool
}

func parseBisect(args []string) (bisectArgs, error) {
	var a bisectArgs
	fs := flag.NewFlagSet("bisect", flag.ContinueOnError)
	check := fs.String("check", "", "an oracle finding kind (bad when it is raised) or a command (exit 0 good, 125 skip, else bad)")
	fs.StringVar(&a.dep, "dep", "", "dependency to bisect: gouroboros, plutigo, ouroboros-mock or dingo")
	fs.StringVar(&a.repo, "repo", "", "local checkout of --dep, read only")
	fs.StringVar(&a.dingo, "dingo", "", "dingo checkout the overlay builds into (not needed for --dep dingo)")
	fs.Uint64Var(&a.seed, "seed", 1, "seed handed to the check")
	fs.StringVar(&a.reg, "registry", "", "developer registry host[:port][/path] (default $"+image.EnvRegistry+")")
	fs.StringVar(&a.arch, "arch", runtime.GOARCH, "GOARCH of the cluster nodes")
	insecure := fs.Bool("registry-insecure", false, "the registry speaks plain HTTP (default $"+image.EnvInsecure+")")
	pos, err := flagx.ParseInterleaved(fs, args)
	if err != nil {
		return a, err
	}
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "registry-insecure" {
			a.insecure = insecure
		}
	})
	if !slices.Contains(bisectDeps, a.dep) {
		return a, fmt.Errorf("bisect: --dep must be one of %v\n%s", bisectDeps, bisectUsage)
	}
	if len(pos) != 1 {
		return a, errors.New("bisect: want one <good>..<bad> range\n" + bisectUsage)
	}
	if a.good, a.bad, err = ibisect.ParseRange(pos[0]); err != nil {
		return a, fmt.Errorf("bisect: %w", err)
	}
	if a.repo == "" || *check == "" {
		return a, errors.New("bisect: --repo and --check are required\n" + bisectUsage)
	}
	if a.dep != devbuild.Dingo && a.dingo == "" {
		return a, fmt.Errorf("bisect: --dep %s builds into dingo; give --dingo", a.dep)
	}
	if slices.Contains(findingKinds, oracle.Kind(*check)) {
		a.kind = *check
	} else {
		a.argv = strings.Fields(*check)
	}
	return a, nil
}

// devBuilder builds one commit of the dependency in reproducible mode.
type devBuilder struct {
	dep, repo, dingo string
	arch             string
	build            func(ctx context.Context, r devbuild.Request) (string, error)
}

// request is the reproducible build of commit. Dingo itself is built at the
// commit; any other dependency overlays that commit onto dingo's own HEAD.
func (d devBuilder) request(commit string) devbuild.Request {
	if d.dep == devbuild.Dingo {
		return devbuild.Request{Dingo: d.repo, Ref: commit, Mode: devbuild.Repro, Arch: d.arch}
	}
	return devbuild.Request{
		Dingo: d.dingo, Mode: devbuild.Repro, Arch: d.arch,
		With: []devbuild.Overlay{{Module: d.dep, Path: d.repo, Sha: commit}},
	}
}

// Build implements ibisect.Builder.
func (d devBuilder) Build(ctx context.Context, commit string) (string, error) {
	return d.build(ctx, d.request(commit))
}

// Run finds the first commit of a dependency range that fails a check, building
// each candidate into a Dingo image.
func Run(ctx context.Context, args []string, out io.Writer) error {
	a, err := parseBisect(args)
	if err != nil {
		return err
	}
	set, err := image.NewSettings(a.reg, a.insecure, os.Getenv, "", image.WithPush())
	if err != nil {
		return fmt.Errorf("bisect: %w", err)
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	b := devbuild.New(devbuild.ExecRunner{}, image.NewResolver(set.Resolver(), log), set, log)
	build := func(ctx context.Context, r devbuild.Request) (string, error) {
		got, err := b.Build(ctx, r)
		if err != nil {
			return "", err
		}
		return flagx.RepoOf(got.Ref) + "@" + got.Digest, nil
	}
	if a.kind != "" {
		return fmt.Errorf("bisect: --check %s needs a finding source: a live devnet run per commit is not wired to this command yet; check with a command that runs it", a.kind)
	}
	p := ibisect.Prober{
		Builder: devBuilder{dep: a.dep, repo: a.repo, dingo: a.dingo, arch: a.arch, build: build},
		Checker: ibisect.CommandCheck{Argv: a.argv, Seed: a.seed},
	}
	return runBisect(ctx, a, devbuild.ExecRunner{}, p, out)
}

// runBisect lists the range and searches it, printing the result.
func runBisect(ctx context.Context, a bisectArgs, git ibisect.Runner, t ibisect.Test, out io.Writer) error {
	commits, err := ibisect.ListCommits(ctx, git, a.repo, a.good, a.bad)
	if err != nil {
		return err
	}
	res, err := ibisect.Search(ctx, commits, t)
	if err != nil {
		return err
	}
	w := &lines.Writer{Out: out}
	w.Printf("%s: %d commits in %s..%s, %d probes\n", a.dep, len(commits), a.good, a.bad, res.Probes)
	if res.Exact {
		w.Printf("first bad commit: %s\n", res.First)
		return w.Err
	}
	w.Printf("the first bad commit is one of (skipped builds hide it):\n")
	for _, c := range res.Candidates {
		w.Printf("  %s\n", c)
	}
	return w.Err
}
