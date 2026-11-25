package devbuild

import (
	"os"
	"testing"
)

func TestOverlayDockerfile(t *testing.T) {
	want, err := os.ReadFile("testdata/overlay.Dockerfile.golden")
	if err != nil {
		t.Fatal(err)
	}
	digest := "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if got := OverlayDockerfile(digest, "/bin/dingo"); string(got) != string(want) {
		t.Fatalf("Dockerfile:\n%s\nwant:\n%s", got, want)
	}
}

func TestBinaryPath(t *testing.T) {
	df := []byte("FROM x AS build\nCOPY --from=build /code/dingo /bin/\nCOPY --from=build /code/other /opt/\n")
	got, err := BinaryPath(df, "dingo")
	if err != nil || got != "/bin/dingo" {
		t.Fatalf("BinaryPath = %q, %v", got, err)
	}
	if _, err := BinaryPath([]byte("FROM x\n"), "dingo"); err == nil {
		t.Fatal("BinaryPath without a COPY of the binary: want error")
	}
}

func TestLdflags(t *testing.T) {
	got := ldflags("github.com/blinklabs-io/dingo", "", "abc123")
	want := "-s -w -X github.com/blinklabs-io/dingo/internal/version.Version= -X github.com/blinklabs-io/dingo/internal/version.CommitHash=abc123"
	if got != want {
		t.Fatalf("ldflags = %q, want %q", got, want)
	}
}
