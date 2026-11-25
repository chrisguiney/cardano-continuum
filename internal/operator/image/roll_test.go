package image

import (
	"testing"
	"time"

	"github.com/chrisguiney/cardano-continuum/api/v1alpha1"
)

func TestRollRecordsDigests(t *testing.T) {
	now := time.Unix(1700000000, 0)
	cur := v1alpha1.ResolvedImage{Ref: "dingo:main", Digest: "sha256:old"}
	next := v1alpha1.ResolvedImage{Ref: "dingo:dev", Digest: "sha256:new"}
	got := Roll(cur, next, now)
	if got == nil || got.From != "sha256:old" || got.To != "sha256:new" || !got.Time.Time.Equal(now) {
		t.Fatalf("Roll = %+v", got)
	}
}

func TestRollNilWhenNothingRolled(t *testing.T) {
	now := time.Unix(1700000000, 0)
	img := v1alpha1.ResolvedImage{Digest: "sha256:a"}
	cases := map[string][2]v1alpha1.ResolvedImage{
		"same digest":      {img, {Ref: "other-tag", Digest: "sha256:a"}},
		"first resolution": {{}, img},
		"unresolved next":  {img, {Ref: "x"}},
	}
	for name, c := range cases {
		if got := Roll(c[0], c[1], now); got != nil {
			t.Errorf("%s: Roll = %+v, want nil", name, got)
		}
	}
}
