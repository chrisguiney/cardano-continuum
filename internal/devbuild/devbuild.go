// Package devbuild builds dingo images that carry a gouroboros, plutigo or
// ouroboros-mock build under test, without touching the developer's source
// trees.
//
// Two modes, chosen per request:
//
//   - Fast cross-compiles dingo on the host inside a go.work staged outside
//     every source tree, then copies the binary over the one in upstream's
//     main image. It accepts uncommitted changes and takes seconds.
//   - Repro stages a clone of dingo at a committed ref, pins each overlay with
//     go get, and builds with dingo's own unmodified Dockerfile. It refuses
//     uncommitted source, so its image is reproducible from its labels.
//
// Both push to the developer registry and label the image with its full
// provenance; the operator's resolver reads the labels into BuildInfo, and
// they are the source of truth (go version -m shows a go.work-replaced module
// as (devel)).
package devbuild

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/chrisguiney/cardano-continuum/api/v1alpha1"
	"github.com/chrisguiney/cardano-continuum/internal/ident"
)

// Dependency groups, as they appear in labels, tags and BuildInfo.Revisions.
const (
	Dingo         = "dingo"
	Gouroboros    = "gouroboros"
	Plutigo       = "plutigo"
	OuroborosMock = "ouroboros-mock"
)

// tagOrder is the order overlays appear in a tag, with the one-letter code
// each uses.
var tagOrder = []struct{ group, code string }{
	{Gouroboros, "g"}, {Plutigo, "p"}, {OuroborosMock, "m"},
}

// Mode is how an image is built.
type Mode string

// The two build modes; the values are the build-mode label values.
const (
	Fast  = Mode(v1alpha1.BuildModeFast)
	Repro = Mode(v1alpha1.BuildModeRepro)
)

// Overlay swaps one dependency of dingo for a build under test.
type Overlay struct {
	// Module is the group: Gouroboros, Plutigo or OuroborosMock.
	Module string
	// Path is a local checkout. Fast mode builds it as it is on disk; repro
	// mode reads its HEAD (and refuses it when dirty) unless Sha is set.
	Path string
	// Sha is the commit to pin in repro mode. Empty means Path's HEAD.
	Sha string
	// Fork is a Go module path that replaces the upstream module in repro
	// mode, for a commit that lives only in a fork.
	Fork string
}

// modulePath is the Go module path of an overlay group.
func modulePath(group string) (string, bool) {
	switch group {
	case Gouroboros, Plutigo, OuroborosMock:
		return ident.UpstreamModuleOrg + "/" + group, true
	}
	return "", false
}

// Request describes one build.
type Request struct {
	// Dingo is a local dingo checkout.
	Dingo string
	// Ref is the commit repro mode builds. Empty means Dingo's HEAD. Fast
	// mode builds the worktree as it is and rejects a Ref.
	Ref string
	// With lists the overlays; none builds plain dingo.
	With []Overlay
	// Mode selects fast or repro.
	Mode Mode
	// Arch is the cluster's GOARCH.
	Arch string
}

// Tag names an image: dev-d<dingo7>[-g<gouroboros7>][-p<plutigo7>]
// [-m<ouroboros-mock7>][-dirty<diffHash6>]. revs maps a group to its commit.
func Tag(dingoSha string, revs map[string]string, dirty bool, diffHash string) string {
	var b strings.Builder
	b.WriteString("dev-d" + short(dingoSha, 7))
	for _, o := range tagOrder {
		if sha := revs[o.group]; sha != "" {
			b.WriteString("-" + o.code + short(sha, 7))
		}
	}
	if dirty {
		b.WriteString("-dirty" + short(diffHash, 6))
	}
	return b.String()
}

func short(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// tag is Tag with the mode's rule applied: a repro build is never dirty.
func (r Request) tag(dingoSha string, revs map[string]string, dirty bool, diffHash string) string {
	return Tag(dingoSha, revs, dirty && r.Mode == Fast, diffHash)
}

// Labels are the image labels that carry the build's provenance. revs must
// hold the dingo commit under Dingo and one commit per overlay.
func (r Request) Labels(revs map[string]string, dirty bool) map[string]string {
	p := ident.LabelPrefix + "/"
	l := map[string]string{
		"org.opencontainers.image.revision": revs[Dingo],
		p + "dirty":                         fmt.Sprint(dirty && r.Mode == Fast),
		p + "build-mode":                    string(r.Mode),
	}
	for _, o := range r.With {
		l[p+o.Module+".revision"] = revs[o.Module]
	}
	return l
}

// labelArgs renders labels as sorted --label arguments.
func labelArgs(l map[string]string) []string {
	keys := make([]string, 0, len(l))
	for k := range l {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []string
	for _, k := range keys {
		out = append(out, "--label", k+"="+l[k])
	}
	return out
}

// validate rejects requests no mode can build.
func (r Request) validate() error {
	if r.Dingo == "" {
		return fmt.Errorf("no dingo checkout")
	}
	if r.Mode != Fast && r.Mode != Repro {
		return fmt.Errorf("unknown build mode %q", r.Mode)
	}
	if r.Mode == Fast && r.Ref != "" {
		return fmt.Errorf("fast mode builds the worktree as it is; a ref needs repro mode")
	}
	seen := map[string]bool{}
	for _, o := range r.With {
		if _, ok := modulePath(o.Module); !ok {
			return fmt.Errorf("unknown overlay module %q", o.Module)
		}
		if seen[o.Module] {
			return fmt.Errorf("overlay %s given twice", o.Module)
		}
		seen[o.Module] = true
		if r.Mode == Fast && o.Path == "" {
			return fmt.Errorf("overlay %s needs a local path in fast mode", o.Module)
		}
		if r.Mode == Repro && o.Path == "" && o.Sha == "" {
			return fmt.Errorf("overlay %s needs a path or a commit", o.Module)
		}
	}
	return nil
}

// overlayModules returns the overlay groups, sorted.
func (r Request) overlayModules() []string {
	var m []string
	for _, o := range r.With {
		m = append(m, o.Module)
	}
	slices.Sort(m)
	return m
}
