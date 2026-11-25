package image

import (
	"testing"

	"github.com/chrisguiney/cardano-continuum/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
)

func TestSelectPrecedence(t *testing.T) {
	def := v1alpha1.ImageSet{v1alpha1.Dingo: "def/dingo:main"}
	net := v1alpha1.ImageSet{v1alpha1.Dingo: "net/dingo:main"}
	tests := []struct {
		name     string
		def, net v1alpha1.ImageSet
		impl     v1alpha1.Implementation
		pool     string
		want     string
		src      Source
	}{
		{"pool wins", def, net, v1alpha1.Dingo, "pool/dingo:x", "pool/dingo:x", FromPool},
		{"network beats default", def, net, v1alpha1.Dingo, "", "net/dingo:main", FromNetwork},
		{"default", def, nil, v1alpha1.Dingo, "", "def/dingo:main", BuiltIn},
		{"impl missing in both", def, net, v1alpha1.CardanoNode, "", "", BuiltIn},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, src := Select(tt.def, tt.net, tt.impl, tt.pool)
			if got != tt.want || src != tt.src {
				t.Fatalf("Select = %q, %v; want %q, %v", got, src, tt.want, tt.src)
			}
		})
	}
}

func TestPullPolicy(t *testing.T) {
	d := "sha256:" + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	tests := []struct {
		ref  string
		want corev1.PullPolicy
	}{
		{"ghcr.io/x/dingo@" + d, corev1.PullIfNotPresent},
		{"ghcr.io/x/dingo:main@" + d, corev1.PullIfNotPresent},
		{"ghcr.io/x/dingo:main", corev1.PullAlways},
		{"localhost:5000/c/dingo:dev-abc", corev1.PullAlways},
		{"ghcr.io/x/dingo", corev1.PullAlways},
	}
	for _, tt := range tests {
		if got := PullPolicy(tt.ref); got != tt.want {
			t.Errorf("PullPolicy(%q) = %v, want %v", tt.ref, got, tt.want)
		}
	}
}

func TestNeedsRollOnlyOnDigest(t *testing.T) {
	cur := v1alpha1.ResolvedImage{Ref: "r/d:main", Digest: "sha256:aa"}
	tests := []struct {
		name string
		next v1alpha1.ResolvedImage
		want bool
	}{
		{"new tag same digest", v1alpha1.ResolvedImage{Ref: "r/d:dev-1", Digest: "sha256:aa"}, false},
		{"same tag new digest", v1alpha1.ResolvedImage{Ref: "r/d:main", Digest: "sha256:bb"}, true},
		{"unresolved next never rolls", v1alpha1.ResolvedImage{Ref: "r/d:main"}, false},
	}
	for _, tt := range tests {
		if got := NeedsRoll(cur, tt.next); got != tt.want {
			t.Errorf("%s: NeedsRoll = %v, want %v", tt.name, got, tt.want)
		}
	}
	if !NeedsRoll(v1alpha1.ResolvedImage{}, cur) {
		t.Error("first resolution should roll")
	}
}
