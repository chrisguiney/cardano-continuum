package devbuild

import (
	"context"
	"io"
	"log"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/chrisguiney/cardano-continuum/api/v1alpha1"
	"github.com/chrisguiney/cardano-continuum/internal/operator/image"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// TestLabelsRoundTripIntoBuildInfo pushes an image carrying Labels to a real
// registry and requires the operator's resolver to read the same provenance
// back into the CRD's BuildInfo.
func TestLabelsRoundTripIntoBuildInfo(t *testing.T) {
	srv := httptest.NewServer(registry.New(registry.Logger(log.New(io.Discard, "", 0))))
	t.Cleanup(srv.Close)
	host := strings.TrimPrefix(srv.URL, "http://")

	r := Request{Mode: Fast, With: []Overlay{{Module: Gouroboros}, {Module: OuroborosMock}}}
	revs := map[string]string{Dingo: dingoSha, Gouroboros: gouroSha, OuroborosMock: mockSha}
	img, err := mutate.ConfigFile(empty.Image, &v1.ConfigFile{
		Architecture: "amd64", OS: "linux",
		Config: v1.Config{Labels: r.Labels(revs, true)},
	})
	if err != nil {
		t.Fatal(err)
	}
	ref := host + "/continuum/dingo:" + Tag(dingoSha, revs, true, "abcdef")
	tr, err := name.ParseReference(ref, name.Insecure)
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.Write(tr, img); err != nil {
		t.Fatal(err)
	}

	set := image.Settings{Registry: host + "/continuum", Insecure: true}
	got, err := image.NewResolver(set.Resolver(), discard()).Resolve(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	want := v1alpha1.BuildInfo{Mode: v1alpha1.BuildModeFast, Dirty: true,
		Revisions: map[string]string{Gouroboros: gouroSha, OuroborosMock: mockSha}}
	if !reflect.DeepEqual(got.Build, want) {
		t.Fatalf("BuildInfo = %+v, want %+v", got.Build, want)
	}
	if got.Commit != dingoSha {
		t.Fatalf("Commit = %q, want the dingo sha", got.Commit)
	}
}

// stubImages resolves the upstream main tag to a fixed digest and echoes any
// other reference.
type stubImages struct{ base string }

func (s stubImages) Resolve(_ context.Context, ref string) (v1alpha1.ResolvedImage, error) {
	if ref == "ghcr.io/blinklabs-io/dingo:main" {
		return v1alpha1.ResolvedImage{Ref: ref, Digest: s.base}, nil
	}
	return v1alpha1.ResolvedImage{Ref: ref, Digest: "sha256:pushed"}, nil
}

func dockerRecorder(t *testing.T, h *hybrid) {
	h.fake = func(dir, name string, args []string) (string, bool) {
		if name == "docker" {
			return "", true
		}
		if name == "go" && len(args) > 0 && (args[0] == "mod" && args[1] == "tidy" || args[0] == "get") {
			return "", true
		}
		return "", false
	}
}

func TestBuildFast(t *testing.T) {
	dingo, gouro := fixture(t)
	h := &hybrid{}
	dockerRecorder(t, h)
	set := image.Settings{Registry: "localhost:5001/continuum", Insecure: true}
	b := New(h, stubImages{base: "sha256:base"}, set, discard())
	r := Request{Dingo: dingo, Mode: Fast, Arch: "arm64", With: []Overlay{{Module: Gouroboros, Path: gouro}}}
	got, err := b.Build(t.Context(), r)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got.Ref, "localhost:5001/continuum/dingo:dev-d") || !strings.Contains(got.Ref, "-g") {
		t.Fatalf("ref = %q", got.Ref)
	}
	if strings.Contains(got.Ref, "-dirty") {
		t.Fatalf("clean trees tagged dirty: %q", got.Ref)
	}
	var build string
	for _, c := range h.calls {
		if strings.HasPrefix(c, "docker build") {
			build = c
		}
	}
	for _, want := range []string{"--provenance=false", "--sbom=false", "--platform linux/arm64", "--label org.opencontainers.image.revision=", "build-mode=fast", "gouroboros.revision=", "-t " + got.Ref} {
		if !strings.Contains(build, want) {
			t.Errorf("docker build %q lacks %q", build, want)
		}
	}
	if !h.called("docker push " + got.Ref) {
		t.Errorf("no push of %s in %v", got.Ref, h.calls)
	}
}

func TestBuildFastDirtyTag(t *testing.T) {
	dingo, gouro := fixture(t)
	write(t, gouro, "gouro.go", "package gouro\n\nconst Name = \"x\"\n")
	h := &hybrid{}
	dockerRecorder(t, h)
	set := image.Settings{Registry: "localhost:5001/continuum", Insecure: true}
	b := New(h, stubImages{base: "sha256:base"}, set, discard())
	got, err := b.Build(t.Context(), Request{Dingo: dingo, Mode: Fast, Arch: "arm64", With: []Overlay{{Module: Gouroboros, Path: gouro}}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.Ref, "-dirty") {
		t.Fatalf("dirty overlay not in tag: %q", got.Ref)
	}
}

func TestBuildReproUsesDingoDockerfile(t *testing.T) {
	dingo, gouro := fixture(t)
	h := &hybrid{}
	dockerRecorder(t, h)
	set := image.Settings{Registry: "localhost:5001/continuum", Insecure: true}
	b := New(h, stubImages{}, set, discard())
	r := Request{Dingo: dingo, Mode: Repro, With: []Overlay{{Module: Gouroboros, Path: gouro}}}
	got, err := b.Build(t.Context(), r)
	if err != nil {
		t.Fatal(err)
	}
	sha := strings.TrimSpace(git(t, dingo, "rev-parse", "HEAD"))
	var build string
	for _, c := range h.calls {
		if strings.HasPrefix(c, "docker build") {
			build = c
		}
	}
	for _, want := range []string{"--build-arg COMMIT_HASH=" + sha, "build-mode=repro", "dirty=false", "-t " + got.Ref} {
		if !strings.Contains(build, want) {
			t.Errorf("docker build %q lacks %q", build, want)
		}
	}
	if strings.Contains(build, "-f ") {
		t.Errorf("repro must use dingo's own Dockerfile: %q", build)
	}
	if h.called("docker build") && strings.Contains(got.Ref, "-dirty") {
		t.Errorf("repro tag dirty: %q", got.Ref)
	}
	if _, err := os.Stat(filepath.Join(dingo, "go.mod")); err != nil {
		t.Fatal(err)
	}
}

func TestBuildNeedsRegistry(t *testing.T) {
	dingo, _ := fixture(t)
	b := New(&hybrid{}, stubImages{}, image.Settings{}, discard())
	if _, err := b.Build(t.Context(), Request{Dingo: dingo, Mode: Repro}); err == nil {
		t.Fatal("want error without a registry")
	}
}
