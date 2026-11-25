package image

import (
	"context"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/chrisguiney/cardano-continuum/api/v1alpha1"
	"github.com/chrisguiney/cardano-continuum/internal/ident"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

type pushed struct {
	host, ref, digest string
}

// push serves a throwaway registry over plain HTTP and pushes one image with
// the given labels and env. host is 127.0.0.1:<port>.
func push(t *testing.T, tag string, labels map[string]string, env []string) pushed {
	t.Helper()
	srv := httptest.NewServer(registry.New())
	t.Cleanup(srv.Close)
	host := strings.TrimPrefix(srv.URL, "http://")
	return pushTo(t, host, tag, labels, env)
}

func pushTo(t *testing.T, host, tag string, labels map[string]string, env []string) pushed {
	t.Helper()
	img, err := mutate.ConfigFile(empty.Image, &v1.ConfigFile{
		Architecture: "amd64", OS: "linux",
		Config: v1.Config{Labels: labels, Env: env},
	})
	if err != nil {
		t.Fatal(err)
	}
	ref := host + "/continuum/dingo:" + tag
	tr, err := name.ParseReference(ref, name.Insecure)
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.Write(tr, img); err != nil {
		t.Fatal(err)
	}
	d, err := img.Digest()
	if err != nil {
		t.Fatal(err)
	}
	return pushed{host: host, ref: ref, digest: d.String()}
}

func newResolver(insecure ...string) *RegistryResolver {
	return NewResolver(ResolverConfig{InsecureRegistries: insecure}, slog.New(slog.DiscardHandler))
}

func TestResolveInsecureRegistry(t *testing.T) {
	p := push(t, "main", map[string]string{revisionLabel: "abc123"}, nil)
	ctx := context.Background()

	got, err := newResolver(p.host).Resolve(ctx, p.ref)
	if err != nil {
		t.Fatal(err)
	}
	if got.Digest != p.digest || got.Ref != p.ref || got.Commit != "abc123" {
		t.Fatalf("Resolve = %+v, want digest %s", got, p.digest)
	}

	for name, list := range map[string][]string{"empty": nil, "other host": {"example.invalid:5000"}} {
		_, err := newResolver(list...).Resolve(ctx, p.ref)
		if err == nil || !strings.Contains(err.Error(), "insecureRegistries") {
			t.Errorf("%s: err = %v, want refusal naming insecureRegistries", name, err)
		}
	}
}

func TestResolveCommit(t *testing.T) {
	tests := []struct {
		name   string
		labels map[string]string
		env    []string
		want   string
	}{
		{"label wins", map[string]string{revisionLabel: "fromlabel"}, []string{"COMMIT_HASH=fromenv"}, "fromlabel"},
		{"env fallback", nil, []string{"PATH=/bin", "COMMIT_HASH=fromenv"}, "fromenv"},
		{"neither", nil, nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := push(t, "main", tt.labels, tt.env)
			got, err := newResolver(p.host).Resolve(context.Background(), p.ref)
			if err != nil {
				t.Fatal(err)
			}
			if got.Commit != tt.want {
				t.Fatalf("Commit = %q, want %q", got.Commit, tt.want)
			}
		})
	}
}

func TestResolveMovedTag(t *testing.T) {
	srv := httptest.NewServer(registry.New())
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")
	a := pushTo(t, host, "main", map[string]string{revisionLabel: "one"}, nil)
	r := newResolver(host)
	first, err := r.Resolve(context.Background(), a.ref)
	if err != nil {
		t.Fatal(err)
	}
	same, err := r.Resolve(context.Background(), a.ref)
	if err != nil || NeedsRoll(first, same) {
		t.Fatalf("unchanged tag must not roll: %v", err)
	}
	b := pushTo(t, host, "main", map[string]string{revisionLabel: "two"}, nil)
	moved, err := r.Resolve(context.Background(), b.ref)
	if err != nil {
		t.Fatal(err)
	}
	if !NeedsRoll(first, moved) || moved.Digest != b.digest {
		t.Fatalf("moved tag must roll: %+v -> %+v", first, moved)
	}
}

func TestBuildLabelsRoundTrip(t *testing.T) {
	p := ident.LabelPrefix
	p1 := push(t, "dev-x", map[string]string{
		revisionLabel:                  "dingo7",
		p + "/gouroboros.revision":     "g7",
		p + "/plutigo.revision":        "p7",
		p + "/ouroboros-mock.revision": "m7",
		p + "/dirty":                   "true",
		p + "/build-mode":              "fast",
	}, nil)
	got, err := newResolver(p1.host).Resolve(context.Background(), p1.ref)
	if err != nil {
		t.Fatal(err)
	}
	want := v1alpha1.BuildInfo{
		Mode: v1alpha1.BuildModeFast, Dirty: true,
		Revisions: map[string]string{"gouroboros": "g7", "plutigo": "p7", "ouroboros-mock": "m7"},
	}
	if got.Build.Mode != want.Mode || got.Build.Dirty != want.Dirty || len(got.Build.Revisions) != 3 {
		t.Fatalf("Build = %+v, want %+v", got.Build, want)
	}
	for k, v := range want.Revisions {
		if got.Build.Revisions[k] != v {
			t.Errorf("Revisions[%s] = %q, want %q", k, got.Build.Revisions[k], v)
		}
	}
	if got.Commit != "dingo7" {
		t.Errorf("Commit = %q", got.Commit)
	}
}

func TestBuildLabelsDefaultsAndErrors(t *testing.T) {
	p := push(t, "main", nil, nil)
	got, err := newResolver(p.host).Resolve(context.Background(), p.ref)
	if err != nil || got.Build.Mode != v1alpha1.BuildModeUpstream || got.Build.Dirty {
		t.Fatalf("unlabelled image: %+v, %v", got.Build, err)
	}
	bad := push(t, "bad", map[string]string{ident.LabelPrefix + "/build-mode": "bogus"}, nil)
	if _, err := newResolver(bad.host).Resolve(context.Background(), bad.ref); err == nil {
		t.Fatal("want error for unknown build-mode")
	}
	d := push(t, "d", map[string]string{ident.LabelPrefix + "/dirty": "yes"}, nil)
	if _, err := newResolver(d.host).Resolve(context.Background(), d.ref); err == nil {
		t.Fatal("want error for unparsable dirty")
	}
}
