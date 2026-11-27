package build

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"regexp"
	"runtime"
	"strings"

	"github.com/chrisguiney/cardano-continuum/internal/cli/internal/flagx"
	"github.com/chrisguiney/cardano-continuum/internal/devbuild"
	"github.com/chrisguiney/cardano-continuum/internal/operator/image"
)

// flagx.ListFlag collects a repeatable string flag.
var shaRe = regexp.MustCompile(`^[0-9a-f]{7,40}$`)

// parseOverlays reads --with module=path|sha and --fork module=gomodule.
// A value naming an existing directory is a checkout; a hex string is a
// commit, which only repro mode can build.
func parseOverlays(with, forks []string) ([]devbuild.Overlay, error) {
	fork := map[string]string{}
	for _, f := range forks {
		m, v, ok := strings.Cut(f, "=")
		if !ok || m == "" || v == "" {
			return nil, fmt.Errorf("--fork %q: want module=gomodule", f)
		}
		fork[m] = v
	}
	var out []devbuild.Overlay
	for _, w := range with {
		m, v, ok := strings.Cut(w, "=")
		if !ok || m == "" || v == "" {
			return nil, fmt.Errorf("--with %q: want module=path|sha", w)
		}
		o := devbuild.Overlay{Module: m, Fork: fork[m]}
		delete(fork, m)
		if st, err := os.Stat(v); err == nil && st.IsDir() {
			o.Path = v
		} else if shaRe.MatchString(v) {
			o.Sha = v
		} else {
			return nil, fmt.Errorf("--with %q: %q is neither a directory nor a commit", w, v)
		}
		out = append(out, o)
	}
	for m := range fork {
		return nil, fmt.Errorf("--fork %s has no matching --with", m)
	}
	return out, nil
}

// Run builds a Dingo image with module overlays and pushes it to the
// developer registry.
func Run(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("build", flag.ContinueOnError)
	var with, forks flagx.ListFlag
	dingo := fs.String("dingo", "", "dingo checkout")
	ref := fs.String("ref", "", "dingo commit for repro mode (default: the checkout's HEAD)")
	mode := fs.String("mode", string(devbuild.Fast), "fast (uncommitted changes allowed) or repro (committed refs, dingo's Dockerfile)")
	arch := fs.String("arch", runtime.GOARCH, "GOARCH of the cluster nodes")
	reg := fs.String("registry", "", "developer registry host[:port][/path] (default $"+image.EnvRegistry+")")
	insecure := fs.Bool("registry-insecure", false, "the registry speaks plain HTTP (default $"+image.EnvInsecure+")")
	fs.Var(&with, "with", "overlay module=path|sha; module is gouroboros, plutigo or ouroboros-mock; repeatable")
	fs.Var(&forks, "fork", "module=gomodule: build the overlay from a fork (repro mode); repeatable")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *dingo == "" {
		return errors.New("build: --dingo is required")
	}
	var flagged *bool
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "registry-insecure" {
			flagged = insecure
		}
	})
	set, err := image.NewSettings(*reg, flagged, os.Getenv, "", image.WithPush())
	if err != nil {
		return fmt.Errorf("build: %w", err)
	}
	overlays, err := parseOverlays(with, forks)
	if err != nil {
		return fmt.Errorf("build: %w", err)
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	b := devbuild.New(devbuild.ExecRunner{}, image.NewResolver(set.Resolver(), log), set, log)
	got, err := b.Build(ctx, devbuild.Request{
		Dingo: *dingo, Ref: *ref, With: overlays, Mode: devbuild.Mode(*mode), Arch: *arch,
	})
	if err != nil {
		return fmt.Errorf("build: %w", err)
	}
	_, err = fmt.Fprintf(out, "%s@%s\n", flagx.RepoOf(got.Ref), got.Digest)
	return err
}
