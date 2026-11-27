// Package tlaflags holds the flags the TLA+ harvesting commands share.
package tlaflags

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/chrisguiney/cardano-continuum/internal/tla"
)

// Flags are the flags harvest, vectors and scenario harvest share.
type Flags struct {
	Repo, Ref, Out, Cache, TLC, Jar string
	Timeout                         time.Duration
	Workers                         int
}

// Register adds the flags to fs.
func (f *Flags) Register(fs *flag.FlagSet) {
	fs.StringVar(&f.Repo, "repo", "", "model repository URL or path")
	fs.StringVar(&f.Ref, "ref", "", "commit to pin; without it --repo must be a directory holding specs/")
	fs.StringVar(&f.Out, "out", "", "output directory")
	fs.StringVar(&f.Cache, "cache", "", "clone cache (default: the user cache directory)")
	fs.StringVar(&f.TLC, "tlc", "models/bin/tlc", "TLC wrapper script")
	fs.StringVar(&f.Jar, "jar", envOr("TLA2TOOLS_JAR", "models/.tools/tla2tools-1.8.0.jar"), "tla2tools jar whose TLC supports -dumpTrace")
	fs.DurationVar(&f.Timeout, "timeout", 4*time.Minute, "bound for each TLC run")
	fs.IntVar(&f.Workers, "workers", 2, "TLC runs in flight")
}

func envOr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

// Resolve checks the required flags, fills the cache default and creates the
// private temp directory TLC runs use; the caller removes it.
func (f *Flags) Resolve(cmd string) (tla.Tools, func(), error) {
	if f.Repo == "" || f.Out == "" {
		return tla.Tools{}, nil, fmt.Errorf("%s: --repo and --out are required", cmd)
	}
	if f.Cache == "" {
		base, err := os.UserCacheDir()
		if err != nil {
			return tla.Tools{}, nil, fmt.Errorf("%s: no cache directory, pass --cache: %w", cmd, err)
		}
		f.Cache = filepath.Join(base, "continuum", "tla")
	}
	tmp, err := os.MkdirTemp("", "continuum-tla-")
	if err != nil {
		return tla.Tools{}, nil, fmt.Errorf("%s: %w", cmd, err)
	}
	// TLC runs in the model's directory, so relative tool paths must be made
	// absolute first.
	tlc, err := filepath.Abs(f.TLC)
	if err != nil {
		return tla.Tools{}, nil, fmt.Errorf("%s: %w", cmd, err)
	}
	jar, err := filepath.Abs(f.Jar)
	if err != nil {
		return tla.Tools{}, nil, fmt.Errorf("%s: %w", cmd, err)
	}
	tools := tla.Tools{TLC: tlc, Jar: jar, Tmp: tmp}
	return tools, func() { _ = os.RemoveAll(tmp) }, nil
}
