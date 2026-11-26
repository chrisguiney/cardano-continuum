package devup

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/chrisguiney/cardano-continuum/api/v1alpha1"
)

func TestCheckMemory(t *testing.T) {
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
	if net.Spec.Budget.MaxPools != 6 || net.Spec.Magic != 42 || net.Spec.GenesisRef != "devnet-genesis" {
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
	got := EngineNode("continuum")
	if !strings.HasPrefix(got, "pool-") || !strings.HasSuffix(got, ".continuum.svc.cluster.local:3002") {
		t.Errorf("EngineNode = %q", got)
	}
}
