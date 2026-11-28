package devup

import (
	"slices"
	"strings"
	"testing"
)

func TestCheckMemory(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, alloc string
		min         int64
		ok, err     bool
	}{
		{"exact", "48Gi", 48, true, false},
		{"kubelet bytes", "49283072Ki", 48, false, false},
		{"plenty", "64Gi", 48, true, false},
		{"short", "16Gi", 48, false, false},
		{"garbage", "lots", 48, false, true},
		{"empty", "", 48, false, true},
	}
	for _, tt := range tests {
		ok, err := CheckMemory(tt.alloc, tt.min)
		if (err != nil) != tt.err || ok != tt.ok {
			t.Errorf("%s: CheckMemory(%q, %d) = %v, %v; want %v, err %v", tt.name, tt.alloc, tt.min, ok, err, tt.ok, tt.err)
		}
	}
}

func TestEngineNode(t *testing.T) {
	t.Parallel()
	got := EngineNode("continuum")
	if !strings.HasPrefix(got, "pool-") || !strings.HasSuffix(got, ".continuum.svc.cluster.local:3002") {
		t.Errorf("EngineNode = %q", got)
	}
}

func TestAttacks(t *testing.T) {
	t.Parallel()
	def, err := Attacks("")
	if err != nil {
		t.Fatal(err)
	}
	if def.Engine != "dust-flood,utxo-contention,malformed-cbor,front-run,double-spend" || len(def.Genesis) != 0 {
		t.Fatalf("default = %+v, want every tier-1 attack a stock genesis supports", def)
	}
	if none, err := Attacks("none"); err != nil || none.Engine != "" || len(none.Genesis) != 0 {
		t.Fatalf("none = %+v, %v", none, err)
	}
	gap, err := Attacks("dust-flood, exunits-block-gap")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"-override=maxTxExUnits.memory=140000000", "-override=maxTxExUnits.steps=20000000000", "-allow-inconsistent-exunits"}
	if gap.Engine != "dust-flood,exunits-block-gap" || !slices.Equal(gap.Genesis, want) {
		t.Fatalf("gap = %+v", gap)
	}
	if _, err := Attacks("teleport"); err == nil {
		t.Fatal("an unknown attack parsed")
	}
}

func TestRunChange(t *testing.T) {
	t.Parallel()
	a := RunIdentity{Image: "reg/continuum:aaaa", Profiles: "preprod", Seed: "00ff", Attacks: "dust-flood"}
	if got := RunChange(a.String(), a); got != "" {
		t.Fatalf("same identity changed: %q", got)
	}
	for _, tc := range []struct {
		mod  func(*RunIdentity)
		want string
	}{
		{func(r *RunIdentity) { r.Image = "reg/continuum:bbbb" }, "engine binary changed"},
		{func(r *RunIdentity) { r.Seed = "0100" }, "seed changed"},
		{func(r *RunIdentity) { r.Profiles = "preview" }, "profiles changed"},
		{func(r *RunIdentity) { r.Attacks = "none" }, "attacks changed"},
		{func(r *RunIdentity) { r.HardFork = "none" }, "hard fork changed"},
		{func(r *RunIdentity) { r.HardFork = "11.0@epoch:5" }, "hard fork changed"},
	} {
		b := a
		tc.mod(&b)
		if got := RunChange(a.String(), b); !strings.HasPrefix(got, tc.want) {
			t.Errorf("RunChange = %q, want %q", got, tc.want)
		}
	}
	if def := (RunIdentity{Image: a.Image, Profiles: a.Profiles, Seed: a.Seed, Attacks: a.Attacks, HardFork: "11.0@epoch:3"}); RunChange(a.String(), def) != "" {
		t.Fatalf("an unset hard fork is not the default: %q", RunChange(a.String(), def))
	}

	if got := RunChange("", a); !strings.Contains(got, "unrecorded") {
		t.Fatalf("a deploy with no recorded identity: %q", got)
	}
	if strings.Contains(a.String(), "00ff") {
		t.Fatal("the identity carries the seed itself")
	}
}
