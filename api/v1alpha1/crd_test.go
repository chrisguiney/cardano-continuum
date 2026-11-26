package v1alpha1

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/chrisguiney/cardano-continuum/internal/ident"
)

// These tests read the controller-gen output, the schema the API server
// validates against, so they fail if the markers and the committed YAML drift.
const crdDir = "../../config/crd/bases"

func loadCRD(t *testing.T, kind string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(crdDir, ident.APIGroup+"_"+strings.ToLower(kind)+"s.yaml"))
	if err != nil {
		t.Fatalf("read CRD for %s: %v", kind, err)
	}
	var c map[string]any
	if err := yaml.Unmarshal(raw, &c); err != nil {
		t.Fatalf("parse CRD for %s: %v", kind, err)
	}
	return c
}

// at walks nested maps by key and fails the test if a key is missing.
func at(t *testing.T, node any, path ...string) any {
	t.Helper()
	cur := node
	for _, k := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			t.Fatalf("at %v: not an object before %q", path, k)
		}
		cur, ok = m[k]
		if !ok {
			t.Fatalf("at %v: missing %q", path, k)
		}
	}
	return cur
}

func schemaOf(t *testing.T, c map[string]any, section string) map[string]any {
	t.Helper()
	versions := at(t, c, "spec", "versions").([]any)
	if len(versions) != 1 {
		t.Fatalf("want exactly one version, got %d", len(versions))
	}
	v := versions[0].(map[string]any)
	if v["name"] != "v1alpha1" {
		t.Errorf("version = %v, want v1alpha1", v["name"])
	}
	return at(t, v, "schema", "openAPIV3Schema", "properties", section).(map[string]any)
}

func enumOf(t *testing.T, schema map[string]any, field string) []string {
	t.Helper()
	raw := at(t, schema, "properties", field, "enum")
	var out []string
	for _, e := range raw.([]any) {
		out = append(out, e.(string))
	}
	return out
}

func requiredOf(t *testing.T, schema map[string]any) []string {
	t.Helper()
	var out []string
	for _, e := range schema["required"].([]any) {
		out = append(out, e.(string))
	}
	sort.Strings(out)
	return out
}

func assertStrings(t *testing.T, what string, got, want []string) {
	t.Helper()
	g := append([]string(nil), got...)
	w := append([]string(nil), want...)
	sort.Strings(g)
	sort.Strings(w)
	if len(g) != len(w) {
		t.Fatalf("%s = %v, want %v", what, got, want)
	}
	for i := range g {
		if g[i] != w[i] {
			t.Errorf("%s = %v, want %v", what, got, want)
			return
		}
	}
}

func TestCRDGroupMatchesIdent(t *testing.T) {
	for _, kind := range []string{"ContinuumNetwork", "ContinuumPool"} {
		c := loadCRD(t, kind)
		if g := at(t, c, "spec", "group"); g != ident.APIGroup {
			t.Errorf("%s spec.group = %v, want %q", kind, g, ident.APIGroup)
		}
		if k := at(t, c, "spec", "names", "kind"); k != kind {
			t.Errorf("spec.names.kind = %v, want %s", k, kind)
		}
	}
}

func TestPoolEnums(t *testing.T) {
	c := loadCRD(t, "ContinuumPool")
	spec := schemaOf(t, c, "spec")
	assertStrings(t, "impl enum", enumOf(t, spec, "impl"),
		[]string{"dingo", "cardano-node", "amaru", "rogue"})
	assertStrings(t, "tier enum", enumOf(t, spec, "tier"),
		[]string{"logical", "validating", "forging"})

	status := schemaOf(t, c, "status")
	assertStrings(t, "phase enum", enumOf(t, status, "phase"),
		[]string{"Pending", "Syncing", "Forging", "Dark"})
}

func TestNetworkEnums(t *testing.T) {
	c := loadCRD(t, "ContinuumNetwork")
	spec := schemaOf(t, c, "spec")
	topo := at(t, spec, "properties", "topology").(map[string]any)
	assertStrings(t, "topology policy enum", enumOf(t, topo, "policy"), []string{"relayed", "mesh"})
	if typ := at(t, spec, "properties", "images", "additionalProperties", "type"); typ != "string" {
		t.Errorf("images values type = %v, want string", typ)
	}
}

func TestRequiredFields(t *testing.T) {
	pool := schemaOf(t, loadCRD(t, "ContinuumPool"), "spec")
	assertStrings(t, "pool spec required", requiredOf(t, pool),
		[]string{"networkRef", "poolId", "impl", "tier"})

	net := schemaOf(t, loadCRD(t, "ContinuumNetwork"), "spec")
	assertStrings(t, "network spec required", requiredOf(t, net),
		[]string{"magic", "genesisRef", "topology", "budget", "epochLength", "securityParam"})
}

func TestNumericMinimums(t *testing.T) {
	net := schemaOf(t, loadCRD(t, "ContinuumNetwork"), "spec")
	for _, f := range []string{"magic", "epochLength", "securityParam"} {
		min := at(t, net, "properties", f, "minimum")
		if min != 1.0 && min != 1 {
			t.Errorf("network %s minimum = %v, want 1", f, min)
		}
	}
}

func TestStatusSubresourceAndPrinterColumns(t *testing.T) {
	for _, kind := range []string{"ContinuumNetwork", "ContinuumPool"} {
		v := at(t, loadCRD(t, kind), "spec", "versions").([]any)[0].(map[string]any)
		if _, ok := at(t, v, "subresources").(map[string]any)["status"]; !ok {
			t.Errorf("%s has no status subresource", kind)
		}
	}

	v := at(t, loadCRD(t, "ContinuumPool"), "spec", "versions").([]any)[0].(map[string]any)
	cols := at(t, v, "additionalPrinterColumns").([]any)
	want := map[string]string{
		"Impl":     ".spec.impl",
		"Tier":     ".spec.tier",
		"Phase":    ".status.phase",
		"Tip Slot": ".status.tip.slot",
	}
	got := map[string]string{}
	for _, raw := range cols {
		col := raw.(map[string]any)
		got[col["name"].(string)] = col["jsonPath"].(string)
	}
	for name, path := range want {
		if got[name] != path {
			t.Errorf("printer column %q jsonPath = %q, want %q", name, got[name], path)
		}
	}
}

// TestBudgetDefaults pins the CRD's budget defaults to the exported
// constants, so an omitted budget is the single-machine budget.
func TestBudgetDefaults(t *testing.T) {
	spec := schemaOf(t, loadCRD(t, "ContinuumNetwork"), "spec")
	b := at(t, spec, "properties", "budget").(map[string]any)
	want := map[string]int{"maxPools": DefaultMaxPools, "cpuMillis": DefaultCPUMillis, "memoryMiB": DefaultMemoryMiB}
	// The object's default must itself satisfy the required fields, or the
	// API server rejects the CRD.
	whole, ok := b["default"].(map[string]any)
	if !ok {
		t.Fatalf("budget default = %v, want an object", b["default"])
	}
	for field, n := range want {
		if whole[field] != n {
			t.Errorf("budget default.%s = %v, want %d", field, whole[field], n)
		}
	}
	for field, n := range want {
		got := at(t, b, "properties", field, "default")
		if got != n {
			t.Errorf("budget.%s default = %v, want %d", field, got, n)
		}
	}
}
