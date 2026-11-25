package imageset

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/chrisguiney/cardano-continuum/api/v1alpha1"
)

var update = flag.Bool("update", false, "rewrite testdata/*.golden")

func pool(name, id string, impl v1alpha1.Implementation, tier v1alpha1.Tier, img string) v1alpha1.ContinuumPool {
	return v1alpha1.ContinuumPool{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       v1alpha1.ContinuumPoolSpec{PoolID: id, Implementation: impl, Tier: tier, Image: img},
	}
}

// fleet is ten dingo forging pools listed out of poolId order, plus pools
// that no dingo selector may reach.
func fleet() []v1alpha1.ContinuumPool {
	var out []v1alpha1.ContinuumPool
	for _, i := range []int{7, 3, 9, 0, 5, 1, 8, 2, 6, 4} {
		out = append(out, pool(fmt.Sprintf("p%d", i), fmt.Sprintf("pool%02d", i), v1alpha1.Dingo, v1alpha1.TierForging, ""))
	}
	out = append(out,
		pool("relay", "pool99", v1alpha1.Dingo, v1alpha1.TierValidating, "old"),
		pool("node", "pool00", v1alpha1.CardanoNode, v1alpha1.TierForging, ""),
	)
	return out
}

func network() *v1alpha1.ContinuumNetwork {
	return &v1alpha1.ContinuumNetwork{
		ObjectMeta: metav1.ObjectMeta{Name: "demo"},
		Spec:       v1alpha1.ContinuumNetworkSpec{Images: v1alpha1.ImageSet{v1alpha1.Dingo: "dingo:main"}},
	}
}

func names(cs []Change) []string {
	var out []string
	for _, c := range cs {
		out = append(out, c.Name)
	}
	return out
}

func TestPercentSelectionDeterministic(t *testing.T) {
	sel := Selector{Percent: 30, Tier: v1alpha1.TierForging}
	want := []string{"p0", "p1", "p2"}
	for range 5 {
		pools := fleet()
		slices.Reverse(pools)
		got, err := Plan(network(), pools, v1alpha1.Dingo, "dingo:dev", sel)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(names(got), want) {
			t.Fatalf("selected %v, want %v", names(got), want)
		}
	}
}

func TestPercentRoundsUp(t *testing.T) {
	for _, tc := range []struct{ pct, n, want int }{{1, 3, 1}, {33, 3, 1}, {34, 3, 2}, {100, 3, 3}, {50, 10, 5}} {
		var pools []v1alpha1.ContinuumPool
		for i := range tc.n {
			pools = append(pools, pool(fmt.Sprintf("p%d", i), fmt.Sprintf("id%d", i), v1alpha1.Dingo, v1alpha1.TierForging, ""))
		}
		got, err := Plan(network(), pools, v1alpha1.Dingo, "x", Selector{Percent: tc.pct})
		if err != nil || len(got) != tc.want {
			t.Errorf("%d%% of %d: %d changes, err %v, want %d", tc.pct, tc.n, len(got), err, tc.want)
		}
	}
}

func TestSelectorFilters(t *testing.T) {
	cases := []struct {
		name string
		sel  Selector
		want []string
	}{
		{"pool id", Selector{PoolID: "pool03"}, []string{"p3"}},
		{"tier", Selector{Tier: v1alpha1.TierValidating}, []string{"relay"}},
		{"tier then percent", Selector{Tier: v1alpha1.TierForging, Percent: 10}, []string{"p0"}},
	}
	for _, tc := range cases {
		got, err := Plan(network(), fleet(), v1alpha1.Dingo, "dingo:dev", tc.sel)
		if err != nil || !slices.Equal(names(got), tc.want) {
			t.Errorf("%s: %v, err %v, want %v", tc.name, names(got), err, tc.want)
		}
	}
}

func TestPlanRefusals(t *testing.T) {
	cases := []struct {
		name string
		sel  Selector
		impl v1alpha1.Implementation
	}{
		{"nothing matches", Selector{PoolID: "nope"}, v1alpha1.Dingo},
		{"pool id with percent", Selector{PoolID: "pool01", Percent: 10}, v1alpha1.Dingo},
		{"percent out of range", Selector{Percent: 101}, v1alpha1.Dingo},
		{"unknown tier", Selector{Tier: "huge"}, v1alpha1.Dingo},
		{"unknown impl", Selector{}, "mystery"},
	}
	for _, tc := range cases {
		if _, err := Plan(network(), fleet(), tc.impl, "x", tc.sel); err == nil {
			t.Errorf("%s: no error", tc.name)
		}
	}
}

func TestNetworkDefaultWithoutSelector(t *testing.T) {
	got, err := Plan(network(), fleet(), v1alpha1.Dingo, "dingo:dev", Selector{})
	if err != nil || len(got) != 1 || got[0].Kind != KindNetwork || got[0].From != "dingo:main" || got[0].To != "dingo:dev" {
		t.Fatalf("got %+v, err %v", got, err)
	}
}

func TestParse(t *testing.T) {
	impl, ref, err := ParseAssignment("dingo=ghcr.io/x/dingo@sha256:ab")
	if err != nil || impl != v1alpha1.Dingo || ref != "ghcr.io/x/dingo@sha256:ab" {
		t.Fatalf("%v %q %v", impl, ref, err)
	}
	for _, bad := range []string{"dingo", "=x", "dingo=", "nope=x"} {
		if _, _, err := ParseAssignment(bad); err == nil {
			t.Errorf("ParseAssignment(%q) accepted", bad)
		}
	}
	if n, err := ParsePercent("30%"); err != nil || n != 30 {
		t.Errorf("30%%: %d %v", n, err)
	}
	for _, bad := range []string{"30", "%", "0%", "101%", "x%", "-5%", "3.5%"} {
		if _, err := ParsePercent(bad); err == nil {
			t.Errorf("ParsePercent(%q) accepted", bad)
		}
	}
}

func TestWriteGolden(t *testing.T) {
	var b bytes.Buffer
	nc, err := Plan(network(), fleet(), v1alpha1.Dingo, "ghcr.io/x/dingo:dev-abc", Selector{})
	if err != nil {
		t.Fatal(err)
	}
	pc, err := Plan(network(), fleet(), v1alpha1.Dingo, "ghcr.io/x/dingo:dev-abc", Selector{Percent: 30, Tier: v1alpha1.TierForging})
	if err != nil {
		t.Fatal(err)
	}
	rc, err := Plan(network(), fleet(), v1alpha1.Dingo, "ghcr.io/x/dingo:dev-abc", Selector{Tier: v1alpha1.TierValidating})
	if err != nil {
		t.Fatal(err)
	}
	for _, cs := range [][]Change{nc, pc, rc} {
		if err := Write(&b, cs, true); err != nil {
			t.Fatal(err)
		}
	}
	if err := Write(&b, pc[:1], false); err != nil {
		t.Fatal(err)
	}
	const path = "testdata/dry-run.golden"
	if *update {
		if err := os.WriteFile(path, b.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test -update)", err)
	}
	if got := b.String(); got != string(want) {
		t.Errorf("output differs from %s:\n%s", path, got)
	}
	if !strings.Contains(b.String(), "nothing applied") {
		t.Error("dry run does not say it applied nothing")
	}
}
