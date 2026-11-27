package profile

import (
	"bytes"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParseFilePopulations(t *testing.T) {
	t.Parallel()
	f, err := ParseFile(bytes.NewReader(open(t, "testdata/populations.yaml")))
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Profiles) != 1 || f.Profiles[0].Name != "casual-wallet" {
		t.Fatalf("profiles %+v", f.Profiles)
	}
	want := []Population{
		{
			Name: "pools", Kind: Pool, Count: 12, ChainCount: 3000,
			Pool: &PoolParams{
				Register: 0.0002, Retire: 0.0001, Update: 0.001, Vote: 0.004,
				Pledge: Spike{Rest: LogNormal{Mu: 25, Sigma: 2}, Point: 0, Share: 0.1},
				Margin: Spike{Rest: Beta{A: 1, B: 30}, Point: 0, Share: 0.2},
				Cost:   Spike{Rest: LogNormal{Mu: 19.2, Sigma: 0.5}, Point: 170_000_000, Share: 0.8},
			},
		},
		{
			Name: "dreps", Kind: DRep, Count: 12, ChainCount: 1000,
			DRep: &DRepParams{Register: 0.001, Retire: 0.0005, Vote: 0.05, Voting: 0.3},
		},
		{
			Name: "contracts", Kind: Contract, ChainCount: 50000,
			Contract: &ContractParams{New: 0.002, Usage: Pareto{Xm: 1, Alpha: 0.8}, TopShare: 0.9},
		},
	}
	if !reflect.DeepEqual(f.Populations, want) {
		t.Fatalf("populations\n got %+v\nwant %+v", f.Populations, want)
	}
	if got := f.Populations[0].Scale(); got != 12.0/3000 {
		t.Errorf("pool scale %v, want %v", got, 12.0/3000)
	}
	if got := f.Populations[2].Scale(); got != 1 {
		t.Errorf("unscaled contract scale %v, want 1", got)
	}
	wantNet := Network{Proposals: map[string]float64{"info": 0.4, "treasury_withdrawals": 1.2}}
	if !reflect.DeepEqual(f.Network, wantNet) {
		t.Errorf("network %+v, want %+v", f.Network, wantNet)
	}
}

// A file without populations is a version-1 file as before, and Parse,
// which the engine uses, reads a file with them as just its profiles.
func TestParseFileOldAndNew(t *testing.T) {
	t.Parallel()
	f, err := ParseFile(bytes.NewReader(open(t, "testdata/minimal.yaml")))
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Populations) != 0 || f.Network.Proposals != nil {
		t.Fatalf("minimal file grew populations: %+v", f)
	}
	ps, err := Parse(bytes.NewReader(open(t, "testdata/populations.yaml")))
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 1 {
		t.Fatalf("Parse returned %d profiles", len(ps))
	}
}

func TestParsePopulationRejects(t *testing.T) {
	t.Parallel()
	tests := []struct{ file, want string }{
		{"population-wallet", `population "q": kind`},
		{"population-mismatch", `population "q": pool: a pool population needs`},
		{"population-negative", `population "q": drep.register_rate`},
		{"population-count", `population "q": count`},
		{"population-share", `population "q": pool.pledge.point_share`},
		{"population-duplicate", `population "q": name`},
		{"network-action", "network.proposal_rate.teleport"},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join("testdata", "invalid", tt.file+".yaml")
			_, err := ParseNamed(path, bytes.NewReader(open(t, path)))
			if err == nil {
				t.Fatal("want error")
			}
			if !strings.Contains(err.Error(), tt.want) || !strings.Contains(err.Error(), path) {
				t.Fatalf("error %q lacks %q or the file name", err, tt.want)
			}
		})
	}
}

func TestSpikeSample(t *testing.T) {
	t.Parallel()
	s := Spike{Rest: Const{V: 5}, Point: 1, Share: 0.25}
	if got := s.Sample(&fixed{v: []float64{0.1}}); got != 1 {
		t.Errorf("draw below the share gave %v, want the point", got)
	}
	if got := s.Sample(&fixed{v: []float64{0.9}}); got != 5 {
		t.Errorf("draw above the share gave %v, want the rest", got)
	}
}

// MarshalFile renders populations and network rates too, and ParseFile
// reads back what it rendered.
func TestMarshalFileRoundTrip(t *testing.T) {
	t.Parallel()
	f, err := ParseFile(bytes.NewReader(open(t, "testdata/populations.yaml")))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := MarshalFile(f)
	if err != nil {
		t.Fatal(err)
	}
	back, err := ParseFile(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("%v\n%s", err, raw)
	}
	if !reflect.DeepEqual(back, f) {
		t.Fatalf("round trip\n got %+v\nwant %+v", back, f)
	}
}

// A file with no populations and no network rates marshals exactly as
// Marshal renders its profiles, so the hash of an old profile file does not
// change.
func TestMarshalFileWithoutPopulations(t *testing.T) {
	t.Parallel()
	f, err := ParseFile(bytes.NewReader(open(t, "testdata/minimal.yaml")))
	if err != nil {
		t.Fatal(err)
	}
	a, err := MarshalFile(f)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Marshal(f.Profiles)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Fatalf("MarshalFile\n%s\nMarshal\n%s", a, b)
	}
}

func TestPoolAndDRepIntents(t *testing.T) {
	t.Parallel()
	for _, k := range []IntentKind{IntentUpdatePool, IntentRetireDRep} {
		if !intents[k] {
			t.Fatalf("%s is not an intent kind", k)
		}
	}
}
