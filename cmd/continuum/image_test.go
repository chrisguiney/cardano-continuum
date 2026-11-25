package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"os"
	"slices"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/chrisguiney/cardano-continuum/api/v1alpha1"
	"github.com/chrisguiney/cardano-continuum/internal/imageset"
)

var update = flag.Bool("update", false, "rewrite testdata/*.golden")

// fakeCluster records the patches the command applies.
type fakeCluster struct {
	net     *v1alpha1.ContinuumNetwork
	pools   []v1alpha1.ContinuumPool
	applied []imageset.Change
}

func (f *fakeCluster) Network(context.Context, string) (*v1alpha1.ContinuumNetwork, error) {
	return f.net, nil
}

func (f *fakeCluster) Pools(context.Context, string) ([]v1alpha1.ContinuumPool, error) {
	return f.pools, nil
}

func (f *fakeCluster) Apply(_ context.Context, c imageset.Change) error {
	f.applied = append(f.applied, c)
	return nil
}

func newFake() *fakeCluster {
	f := &fakeCluster{net: &v1alpha1.ContinuumNetwork{
		ObjectMeta: metav1.ObjectMeta{Name: "demo"},
		Spec:       v1alpha1.ContinuumNetworkSpec{Images: v1alpha1.ImageSet{v1alpha1.Dingo: "dingo:main"}},
	}}
	for _, i := range []int{4, 1, 3, 0, 2} {
		f.pools = append(f.pools, v1alpha1.ContinuumPool{
			ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("p%d", i)},
			Spec: v1alpha1.ContinuumPoolSpec{
				PoolID: fmt.Sprintf("pool%d", i), Implementation: v1alpha1.Dingo, Tier: v1alpha1.TierForging,
			},
		})
	}
	return f
}

func runImage(t *testing.T, f *fakeCluster, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	open := func(string, string) (imageCluster, error) { return f, nil }
	err := cmdImage(t.Context(), append([]string{"set"}, args...), &out, open)
	return out.String(), err
}

func TestImageSetPatchesSelectedPools(t *testing.T) {
	f := newFake()
	// Flags may follow the positional argument, as in the usage line.
	if _, err := runImage(t, f, "dingo=dingo:dev", "--network", "demo", "--pools", "40%"); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, c := range f.applied {
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
	f := newFake()
	if _, err := runImage(t, f, "--network", "demo", "dingo=dingo:dev"); err != nil {
		t.Fatal(err)
	}
	if len(f.applied) != 1 || f.applied[0].Kind != imageset.KindNetwork || f.applied[0].To != "dingo:dev" {
		t.Fatalf("applied %+v", f.applied)
	}
}

func TestImageSetDryRunGolden(t *testing.T) {
	f := newFake()
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
	if len(f.applied) != 0 {
		t.Fatalf("dry run applied %+v", f.applied)
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
		f := newFake()
		if _, err := runImage(t, f, args...); err == nil {
			t.Errorf("%v accepted", args)
		}
		if len(f.applied) != 0 {
			t.Errorf("%v applied %+v", args, f.applied)
		}
	}
}
