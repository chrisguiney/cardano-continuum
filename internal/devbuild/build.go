package devbuild

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/chrisguiney/cardano-continuum/api/v1alpha1"
	"github.com/chrisguiney/cardano-continuum/internal/ident"
	"github.com/chrisguiney/cardano-continuum/internal/operator/image"
)

// Builder builds and pushes overlay images. The registry settings are the
// ones every client of the developer registry shares.
type Builder struct {
	run    Runner
	images image.Resolver
	reg    image.Settings
	log    *slog.Logger
}

// New returns a Builder. images resolves the upstream main image to a digest
// and reads the pushed image back; a nil log discards.
func New(run Runner, images image.Resolver, reg image.Settings, log *slog.Logger) *Builder {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Builder{run: run, images: images, reg: reg, log: log.With("component", "devbuild")}
}

// revisions returns each group's commit (dingo's under Dingo), whether any
// tree is dirty, and a combined hash of the changes. Repro mode builds
// committed refs only, so a dirty overlay checkout is an error there.
func (b *Builder) revisions(ctx context.Context, r Request) (revs map[string]string, dirty bool, hash string, err error) {
	revs = map[string]string{}
	rev := func(path, ref string) (string, error) {
		out, err := gitOut(ctx, path, "rev-parse", "--verify", ref+"^{commit}")
		return strings.TrimSpace(string(out)), err
	}
	ref := "HEAD"
	if r.Ref != "" {
		ref = r.Ref
	}
	if revs[Dingo], err = rev(r.Dingo, ref); err != nil {
		return nil, false, "", err
	}
	trees := map[string]string{}
	if r.Mode == Fast {
		trees[Dingo] = r.Dingo
	}
	for _, o := range r.With {
		sha, err := b.overlayRev(rev, r.Mode, o)
		if err != nil {
			return nil, false, "", err
		}
		revs[o.Module] = sha
		if o.Path != "" {
			trees[o.Module] = o.Path
		}
	}
	h := sha256.New()
	for _, g := range append([]string{Dingo}, r.overlayModules()...) {
		path, ok := trees[g]
		if !ok {
			continue
		}
		d, dh, err := DirtyState(ctx, path)
		if err != nil {
			return nil, false, "", err
		}
		if !d {
			continue
		}
		if r.Mode == Repro {
			return nil, false, "", fmt.Errorf("%s at %s has uncommitted changes: repro mode builds committed refs only", g, path)
		}
		dirty = true
		h.Write([]byte(g + dh))
	}
	if dirty {
		hash = hex.EncodeToString(h.Sum(nil))
	}
	return revs, dirty, hash, nil
}

// overlayRev resolves an overlay's commit: Sha when it names a commit that
// repro mode pins, otherwise HEAD of Path. In fast mode Sha is only a check
// that the checkout is where the caller thinks it is.
func (b *Builder) overlayRev(rev func(path, ref string) (string, error), m Mode, o Overlay) (string, error) {
	if o.Path == "" {
		return o.Sha, nil
	}
	if m == Repro && o.Sha != "" {
		return rev(o.Path, o.Sha)
	}
	head, err := rev(o.Path, "HEAD")
	if err != nil {
		return "", err
	}
	if o.Sha != "" && !strings.HasPrefix(head, o.Sha) {
		return "", fmt.Errorf("overlay %s is at %s, not %s", o.Module, short(head, 7), o.Sha)
	}
	return head, nil
}

// compile cross-compiles dingo out of the staged workspace the way dingo's
// Makefile does: CGO off, the dingo_extra_plugins tag and its ldflags.
func (b *Builder) compile(ctx context.Context, dingo, goWork, out, goos, goarch, version, commit string) error {
	mod, err := b.run.Run(ctx, dingo, []string{"GOWORK=off"}, "go", "list", "-m")
	if err != nil {
		return fmt.Errorf("read dingo module path: %w", err)
	}
	env := []string{"GOWORK=" + goWork, "GOOS=" + goos, "GOARCH=" + goarch, "CGO_ENABLED=0"}
	_, err = b.run.Run(ctx, dingo, env, "go", "build", "-tags", "dingo_extra_plugins",
		"-ldflags", ldflags(strings.TrimSpace(mod), version, commit), "-o", out, "./cmd/dingo")
	if err != nil {
		return fmt.Errorf("build dingo: %w", err)
	}
	return nil
}

// Build stages, builds and pushes the image for r, then reads it back through
// the resolver so the caller sees the digest and BuildInfo the operator will.
func (b *Builder) Build(ctx context.Context, r Request) (v1alpha1.ResolvedImage, error) {
	var none v1alpha1.ResolvedImage
	if err := r.validate(); err != nil {
		return none, err
	}
	if b.reg.Registry == "" {
		return none, fmt.Errorf("no registry: set %s or pass the registry flag", image.EnvRegistry)
	}
	if r.Arch == "" {
		r.Arch = runtime.GOARCH
	}
	revs, dirty, hash, err := b.revisions(ctx, r)
	if err != nil {
		return none, err
	}
	tmp, err := os.MkdirTemp("", "continuum-devbuild-")
	if err != nil {
		return none, fmt.Errorf("create staging dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmp) }()

	ref := b.reg.Registry + "/dingo:" + r.tag(revs[Dingo], revs, dirty, hash)
	// Attestations turn the pushed image into an index with an extra
	// "unknown" platform entry, which the resolver cannot read labels from.
	build := []string{"build", "--provenance=false", "--sbom=false"}
	ctxDir := ""
	switch r.Mode {
	case Fast:
		if ctxDir, err = b.buildFast(ctx, r, revs[Dingo], tmp); err != nil {
			return none, err
		}
		build = append(build, "--platform", "linux/"+r.Arch)
	case Repro:
		if err = b.StageRepro(ctx, r, tmp); err != nil {
			return none, err
		}
		ctxDir = filepath.Join(tmp, "dingo")
		build = append(build, "--build-arg", "COMMIT_HASH="+revs[Dingo])
	}
	build = append(build, labelArgs(r.Labels(revs, dirty))...)
	build = append(build, "-t", ref, ctxDir)
	if _, err := b.run.Run(ctx, "", nil, "docker", build...); err != nil {
		return none, fmt.Errorf("docker build %s: %w", ref, err)
	}
	if _, err := b.run.Run(ctx, "", nil, "docker", "push", ref); err != nil {
		return none, fmt.Errorf("docker push %s: %w", ref, err)
	}
	got, err := b.images.Resolve(ctx, ref)
	if err != nil {
		return none, fmt.Errorf("read back %s: %w", ref, err)
	}
	b.log.Info("image built", "ref", ref, "digest", got.Digest, "mode", string(r.Mode), "dirty", dirty)
	return got, nil
}

// buildFast stages the workspace, compiles dingo and writes the docker
// context (binary plus the overlay Dockerfile) under tmp.
func (b *Builder) buildFast(ctx context.Context, r Request, dingoSha, tmp string) (string, error) {
	stage := filepath.Join(tmp, "work")
	ctxDir := filepath.Join(tmp, "image")
	for _, d := range []string{stage, ctxDir} {
		if err := os.Mkdir(d, 0o750); err != nil {
			return "", fmt.Errorf("create %s: %w", d, err)
		}
	}
	goWork, err := b.StageFast(ctx, r, stage)
	if err != nil {
		return "", err
	}
	df, err := os.ReadFile(filepath.Join(r.Dingo, "Dockerfile"))
	if err != nil {
		return "", fmt.Errorf("read dingo Dockerfile: %w", err)
	}
	binPath, err := BinaryPath(df, "dingo")
	if err != nil {
		return "", err
	}
	base, err := b.images.Resolve(ctx, ident.UpstreamDingoImage+":main")
	if err != nil {
		return "", fmt.Errorf("pin upstream main image: %w", err)
	}
	version := ""
	if out, err := gitOut(ctx, r.Dingo, "describe", "--tags", "--exact-match"); err == nil {
		version = strings.TrimSpace(string(out))
	}
	if err := b.compile(ctx, r.Dingo, goWork, filepath.Join(ctxDir, "dingo"), "linux", r.Arch, version, dingoSha); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(ctxDir, "Dockerfile"), OverlayDockerfile(base.Digest, binPath), 0o600); err != nil {
		return "", fmt.Errorf("write overlay Dockerfile: %w", err)
	}
	return ctxDir, nil
}

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
