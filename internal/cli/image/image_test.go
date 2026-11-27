package image

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"slices"
	"testing"

	"github.com/chrisguiney/cardano-continuum/internal/cli/internal/kube"
	"github.com/chrisguiney/cardano-continuum/internal/cli/internal/kubetest"
	"github.com/chrisguiney/cardano-continuum/internal/imageset"
)

var update = flag.Bool("update", false, "rewrite testdata/*.golden")

func runImage(t *testing.T, f *kubetest.Fake, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	open := func(string, string) (kube.Client, error) { return f, nil }
	err := RunWith(t.Context(), append([]string{"set"}, args...), &out, open)
	return out.String(), err
}

func TestImageSetPatchesSelectedPools(t *testing.T) {
	f := kubetest.New()
	// Flags may follow the positional argument, as in the usage line.
	if _, err := runImage(t, f, "dingo=dingo:dev", "--network", "demo", "--pools", "40%"); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, c := range f.Applied {
		if c.Kind != imageset.KindPool || c.To != "dingo:dev" {
			t.Fatalf("unexpected patch %+v", c)
		}
		got = append(got, c.Name)
	}
	if want := []string{"p0", "p1"}; !slices.Equal(got, want) {
		t.Fatalf("patched %v, want %v", got, want)
	}
}

func TestImageSetNetworkDefault(t *testing.T) {
	f := kubetest.New()
	if _, err := runImage(t, f, "--network", "demo", "dingo=dingo:dev"); err != nil {
		t.Fatal(err)
	}
	if len(f.Applied) != 1 || f.Applied[0].Kind != imageset.KindNetwork || f.Applied[0].To != "dingo:dev" {
		t.Fatalf("applied %+v", f.Applied)
	}
}

func TestImageSetDryRunGolden(t *testing.T) {
	f := kubetest.New()
	var all bytes.Buffer
	for _, args := range [][]string{
		{"dingo=ghcr.io/x/dingo:dev-abc", "--network", "demo", "--dry-run"},
		{"dingo=ghcr.io/x/dingo:dev-abc", "--network", "demo", "--pools", "40%", "--dry-run"},
		{"dingo=ghcr.io/x/dingo:dev-abc", "--network", "demo", "--pool", "pool3", "--dry-run"},
		{"dingo=ghcr.io/x/dingo:dev-abc", "--network", "demo", "--tier", "forging", "--dry-run"},
	} {
		out, err := runImage(t, f, args...)
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		fmt.Fprintf(&all, "$ continuum image set %v\n%s", args, out)
	}
	if len(f.Applied) != 0 {
		t.Fatalf("dry run applied %+v", f.Applied)
	}
	const path = "testdata/image-set-dry-run.golden"
	if *update {
		if err := os.WriteFile(path, all.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test -update)", err)
	}
	if all.String() != string(want) {
		t.Errorf("output differs from %s (go test -update rewrites it):\n%s", path, all.String())
	}
}

func TestImageSetUsageErrors(t *testing.T) {
	for _, args := range [][]string{
		{},
		{"dingo=x"},
		{"--network", "demo"},
		{"--network", "demo", "dingo=x", "--pools", "30"},
		{"--network", "demo", "dingo=x", "--pools", "30%", "--pool", "pool1"},
		{"--network", "demo", "dingo=x", "extra"},
		{"--network", "demo", "bogus=x"},
	} {
		f := kubetest.New()
		if _, err := runImage(t, f, args...); err == nil {
			t.Errorf("%v accepted", args)
		}
		if len(f.Applied) != 0 {
			t.Errorf("%v applied %+v", args, f.Applied)
		}
	}
}
