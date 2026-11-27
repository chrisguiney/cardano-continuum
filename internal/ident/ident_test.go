package ident

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestValues(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, got, want string
	}{
		{"Module", Module, "github.com/chrisguiney/cardano-continuum"},
		{"ImageRepo", ImageRepo, "ghcr.io/chrisguiney"},
		{"APIGroup", APIGroup, "continuum.blinklabs.io"},
		{"LabelPrefix", LabelPrefix, "continuum.blinklabs.io"},
		{"MetricsNS", MetricsNS, "continuum"},
		{"UpstreamModuleOrg", UpstreamModuleOrg, "github.com/blinklabs-io"},
		{"UpstreamDingoImage", UpstreamDingoImage, "ghcr.io/blinklabs-io/dingo"},
	}
	for _, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("%s = %q, want %q", tt.name, tt.got, tt.want)
		}
	}
}

// TestMakefileMirrors keeps the Makefile's copy of each identifier equal to
// the Go constant. Make cannot import Go, so the two are kept in step here.
func TestMakefileMirrors(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("../../Makefile")
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`(?m)^([A-Z_]+)\s*:=\s*(\S+)\s*$`)
	vars := map[string]string{}
	for _, m := range re.FindAllStringSubmatch(string(raw), -1) {
		vars[m[1]] = m[2]
	}
	want := map[string]string{
		"MODULE":       Module,
		"IMAGE_REPO":   ImageRepo,
		"API_GROUP":    APIGroup,
		"LABEL_PREFIX": LabelPrefix,
		"METRICS_NS":   MetricsNS,
	}
	for name, value := range want {
		if got, ok := vars[name]; !ok {
			t.Errorf("Makefile has no %s := ...", name)
		} else if got != value {
			t.Errorf("Makefile %s = %q, want %q", name, got, value)
		}
	}
	if t.Failed() {
		t.Log(strings.Join(keys(vars), ", "))
	}
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
