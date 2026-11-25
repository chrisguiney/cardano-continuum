package image

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"

	"github.com/chrisguiney/cardano-continuum/api/v1alpha1"
	"github.com/chrisguiney/cardano-continuum/internal/ident"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

const (
	revisionLabel = "org.opencontainers.image.revision"
	commitEnv     = "COMMIT_HASH"
)

// Resolver turns an image reference into the digest it names now.
type Resolver interface {
	Resolve(ctx context.Context, ref string) (v1alpha1.ResolvedImage, error)
}

// ResolverConfig is the operator registry configuration.
type ResolverConfig struct {
	// InsecureRegistries lists host:port registries that may be reached over
	// plain HTTP. Every other registry is HTTPS only.
	InsecureRegistries []string
	// Mirrors maps a registry host as image references name it to the host
	// the operator fetches manifests from. A developer registry is pushed
	// to as localhost:<port>, which inside the operator's pod is the pod
	// itself; the nodes' containerd maps it separately (hack/kind-up.sh).
	// The scheme follows the original host's InsecureRegistries entry.
	Mirrors map[string]string
}

// ParseMirrors reads "from=to" pairs separated by commas, each a bare
// host[:port]. An empty string is no mirrors.
func ParseMirrors(s string) (map[string]string, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	out := map[string]string{}
	for _, pair := range strings.Split(s, ",") {
		from, to, ok := strings.Cut(strings.TrimSpace(pair), "=")
		if !ok || !bareHost(from) || !bareHost(to) {
			return nil, fmt.Errorf("registry mirror %q: want host[:port]=host[:port]", pair)
		}
		out[from] = to
	}
	return out, nil
}

func bareHost(h string) bool { return h != "" && !strings.ContainsAny(h, "/=@ ") }

// RegistryResolver resolves references with go-containerregistry.
type RegistryResolver struct {
	cfg ResolverConfig
	log *slog.Logger
}

// NewResolver returns a resolver for cfg.
func NewResolver(cfg ResolverConfig, log *slog.Logger) *RegistryResolver {
	return &RegistryResolver{cfg: cfg, log: log.With("component", "image_resolver")}
}

// Resolve reads the manifest for ref and returns its digest, source commit
// and build provenance. A missing commit is logged, not an error: third-party
// images carry neither label nor COMMIT_HASH.
func (r *RegistryResolver) Resolve(ctx context.Context, ref string) (v1alpha1.ResolvedImage, error) {
	parsed, err := r.parse(ref)
	if err != nil {
		return v1alpha1.ResolvedImage{}, err
	}
	desc, err := remote.Get(r.mirrored(parsed), remote.WithContext(ctx))
	if err != nil {
		return v1alpha1.ResolvedImage{}, fmt.Errorf("resolve %s: %w", ref, err)
	}
	img, err := desc.Image()
	if err != nil {
		return v1alpha1.ResolvedImage{}, fmt.Errorf("read image %s: %w", ref, err)
	}
	cfg, err := img.ConfigFile()
	if err != nil {
		return v1alpha1.ResolvedImage{}, fmt.Errorf("read config %s: %w", ref, err)
	}
	build, err := parseBuild(cfg.Config.Labels)
	if err != nil {
		return v1alpha1.ResolvedImage{}, fmt.Errorf("labels of %s: %w", ref, err)
	}
	commit := commitOf(cfg.Config.Labels, cfg.Config.Env)
	if commit == "" {
		r.log.Warn("image has no source commit", "ref", ref)
	}
	return v1alpha1.ResolvedImage{Ref: ref, Digest: desc.Digest.String(), Commit: commit, Build: build}, nil
}

// parse refuses plain HTTP unless the registry is listed. Without
// name.Insecure, name.ParseReference still picks http for loopback and
// private hosts, so the scheme is checked rather than assumed.
func (r *RegistryResolver) parse(ref string) (name.Reference, error) {
	parsed, err := name.ParseReference(ref)
	if err != nil {
		return nil, fmt.Errorf("parse image reference %q: %w", ref, err)
	}
	host := parsed.Context().RegistryStr()
	if slices.Contains(r.cfg.InsecureRegistries, host) {
		return name.ParseReference(ref, name.Insecure)
	}
	if parsed.Context().Scheme() == "http" {
		return nil, fmt.Errorf("registry %s is plain HTTP and not listed in insecureRegistries", host)
	}
	return parsed, nil
}

// mirrored is ref with its registry replaced by the configured mirror.
func (r *RegistryResolver) mirrored(ref name.Reference) name.Reference {
	to, ok := r.cfg.Mirrors[ref.Context().RegistryStr()]
	if !ok {
		return ref
	}
	var opts []name.Option
	if ref.Context().Scheme() == "http" {
		opts = append(opts, name.Insecure)
	}
	reg, err := name.NewRegistry(to, opts...)
	if err != nil {
		// ParseMirrors admitted only bare hosts, which NewRegistry accepts.
		return ref
	}
	repo := ref.Context()
	repo.Registry = reg
	switch t := ref.(type) {
	case name.Tag:
		return repo.Tag(t.TagStr())
	case name.Digest:
		return repo.Digest(t.DigestStr())
	}
	return ref
}

func commitOf(labels map[string]string, env []string) string {
	if v := labels[revisionLabel]; v != "" {
		return v
	}
	for _, e := range env {
		if v, ok := strings.CutPrefix(e, commitEnv+"="); ok {
			return v
		}
	}
	return ""
}

// parseBuild reads the continuum build labels. An image without them is an
// upstream build. Unknown values are rejected, not guessed.
func parseBuild(labels map[string]string) (v1alpha1.BuildInfo, error) {
	prefix := ident.LabelPrefix + "/"
	b := v1alpha1.BuildInfo{Mode: v1alpha1.BuildModeUpstream}
	for k, v := range labels {
		key, ok := strings.CutPrefix(k, prefix)
		if !ok {
			continue
		}
		switch key {
		case "build-mode":
			mode := v1alpha1.BuildMode(v)
			known := []v1alpha1.BuildMode{v1alpha1.BuildModeUpstream, v1alpha1.BuildModeFast, v1alpha1.BuildModeRepro}
			if !slices.Contains(known, mode) {
				return b, fmt.Errorf("unknown build-mode %q", v)
			}
			b.Mode = mode
		case "dirty":
			d, err := strconv.ParseBool(v)
			if err != nil {
				return b, fmt.Errorf("parse dirty label: %w", err)
			}
			b.Dirty = d
		default:
			if group, ok := strings.CutSuffix(key, ".revision"); ok && group != "" {
				if b.Revisions == nil {
					b.Revisions = map[string]string{}
				}
				b.Revisions[group] = v
			}
		}
	}
	return b, nil
}
