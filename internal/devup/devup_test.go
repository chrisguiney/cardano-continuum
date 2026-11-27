package devup

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/chrisguiney/cardano-continuum/api/v1alpha1"
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

func TestPlanCheck(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		p    Plan
		want string
	}{
		{"default", Plan{Nodes: 3, Dingo: 1}, ""},
		{"no pools", Plan{}, "at least one"},
		{"negative dingo", Plan{Nodes: 1, Dingo: -1}, "negative"},
		{"over max", Plan{Nodes: MaxPools, Dingo: 1}, "at most"},
		{"at max", Plan{Nodes: MaxPools - 1, Dingo: 1}, ""},
	}
	for _, tt := range tests {
		err := tt.p.Check()
		switch {
		case tt.want == "" && err != nil:
			t.Errorf("%s: %v", tt.name, err)
		case tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)):
			t.Errorf("%s: err = %v, want mention of %q", tt.name, err, tt.want)
		}
	}
}

func TestRender(t *testing.T) {
	t.Parallel()
	out, err := Render(Plan{Nodes: 3, Dingo: 2, Magic: 42, EpochLength: 500, SecurityParam: 5})
	if err != nil {
		t.Fatal(err)
	}
	var list struct {
		Kind  string            `json:"kind"`
		Items []json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		t.Fatal(err)
	}
	if list.Kind != "List" || len(list.Items) != 6 {
		t.Fatalf("kind %q with %d items, want a List of 1 network and 5 pools", list.Kind, len(list.Items))
	}
	var net v1alpha1.ContinuumNetwork
	if err := json.Unmarshal(list.Items[0], &net); err != nil {
		t.Fatal(err)
	}
	if net.Spec.Budget != (v1alpha1.BudgetSpec{MaxPools: v1alpha1.DefaultMaxPools, CPUMillis: v1alpha1.DefaultCPUMillis, MemoryMiB: v1alpha1.DefaultMemoryMiB}) || net.Spec.Magic != 42 || net.Spec.GenesisRef != "devnet-genesis" {
		t.Errorf("network spec = %+v", net.Spec)
	}
	var impls []v1alpha1.Implementation
	for i, raw := range list.Items[1:] {
		var p v1alpha1.ContinuumPool
		if err := json.Unmarshal(raw, &p); err != nil {
			t.Fatal(err)
		}
		impls = append(impls, p.Spec.Implementation)
		if want := "continuum-pool-" + string(rune('0'+i)); p.Spec.PoolID != want || p.Spec.KeysSecret != "keys-"+string(rune('0'+i)) {
			t.Errorf("pool %d = %+v", i, p.Spec)
		}
	}
	want := []v1alpha1.Implementation{v1alpha1.CardanoNode, v1alpha1.CardanoNode, v1alpha1.CardanoNode, v1alpha1.Dingo, v1alpha1.Dingo}
	for i := range want {
		if impls[i] != want[i] {
			t.Fatalf("implementations = %v, want %v: the engine reaches pool 0, so cardano-node pools come first", impls, want)
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
	} {
		b := a
		tc.mod(&b)
		if got := RunChange(a.String(), b); !strings.HasPrefix(got, tc.want) {
			t.Errorf("RunChange = %q, want %q", got, tc.want)
		}
	}
	if got := RunChange("", a); !strings.Contains(got, "unrecorded") {
		t.Fatalf("a deploy with no recorded identity: %q", got)
	}
	if strings.Contains(a.String(), "00ff") {
		t.Fatal("the identity carries the seed itself")
	}
}
