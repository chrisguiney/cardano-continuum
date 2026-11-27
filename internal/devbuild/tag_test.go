package devbuild

import (
	"testing"

	"github.com/chrisguiney/cardano-continuum/api/v1alpha1"
	"github.com/chrisguiney/cardano-continuum/internal/ident"
)

const (
	dingoSha  = "1234567890abcdef1234567890abcdef12345678"
	gouroSha  = "abcdef1234567890abcdef1234567890abcdef12"
	plutiSha  = "fedcba0987654321fedcba0987654321fedcba09"
	mockSha   = "0a1b2c3d4e5f60718293a4b5c6d7e8f901234567"
	diffHash6 = "9f8e7d6c5b4a39281706"
)

func TestTagComputation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		revs  map[string]string
		dirty bool
		want  string
	}{
		{"dingo only", nil, false, "dev-d1234567"},
		{"gouroboros", map[string]string{Gouroboros: gouroSha}, false, "dev-d1234567-gabcdef1"},
		{"with plutigo", map[string]string{Gouroboros: gouroSha, Plutigo: plutiSha}, false, "dev-d1234567-gabcdef1-pfedcba0"},
		{"plutigo alone", map[string]string{Plutigo: plutiSha}, false, "dev-d1234567-pfedcba0"},
		{"all three", map[string]string{Gouroboros: gouroSha, Plutigo: plutiSha, OuroborosMock: mockSha}, false, "dev-d1234567-gabcdef1-pfedcba0-m0a1b2c3"},
		{"dirty", map[string]string{Gouroboros: gouroSha}, true, "dev-d1234567-gabcdef1-dirty9f8e7d"},
		{"short sha kept whole", map[string]string{Gouroboros: "abc"}, false, "dev-d1234567-gabc"},
	}
	for _, tt := range tests {
		if got := Tag(dingoSha, tt.revs, tt.dirty, diffHash6); got != tt.want {
			t.Errorf("%s: Tag = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestReproNeverDirty(t *testing.T) {
	t.Parallel()
	r := Request{Mode: Repro}
	if got := r.tag(dingoSha, map[string]string{Gouroboros: gouroSha}, true, diffHash6); got != "dev-d1234567-gabcdef1" {
		t.Fatalf("repro tag = %q, want no dirty suffix", got)
	}
	r.Mode = Fast
	if got := r.tag(dingoSha, nil, true, diffHash6); got != "dev-d1234567-dirty9f8e7d" {
		t.Fatalf("fast tag = %q", got)
	}
}

func TestLabels(t *testing.T) {
	t.Parallel()
	r := Request{Mode: Fast, With: []Overlay{{Module: Gouroboros}, {Module: Plutigo}}}
	revs := map[string]string{Dingo: dingoSha, Gouroboros: gouroSha, Plutigo: plutiSha}
	got := r.Labels(revs, true)
	p := ident.LabelPrefix + "/"
	want := map[string]string{
		"org.opencontainers.image.revision": dingoSha,
		p + "gouroboros.revision":           gouroSha,
		p + "plutigo.revision":              plutiSha,
		p + "dirty":                         "true",
		p + "build-mode":                    "fast",
	}
	if len(got) != len(want) {
		t.Fatalf("labels = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("label %s = %q, want %q", k, got[k], v)
		}
	}
	r.Mode = Repro
	if l := r.Labels(revs, true); l[p+"dirty"] != "false" || l[p+"build-mode"] != string(v1alpha1.BuildModeRepro) {
		t.Errorf("repro labels = %v, want dirty=false build-mode=repro", l)
	}
}
