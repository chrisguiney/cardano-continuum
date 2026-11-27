package build

import (
	"reflect"
	"testing"

	"github.com/chrisguiney/cardano-continuum/internal/devbuild"
)

func TestParseOverlays(t *testing.T) {
	dir := t.TempDir()
	got, err := parseOverlays(
		[]string{"gouroboros=" + dir, "plutigo=abcdef1"},
		[]string{"plutigo=github.com/fork/plutigo"})
	if err != nil {
		t.Fatal(err)
	}
	want := []devbuild.Overlay{
		{Module: "gouroboros", Path: dir},
		{Module: "plutigo", Sha: "abcdef1", Fork: "github.com/fork/plutigo"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("overlays = %+v, want %+v", got, want)
	}
	for name, with := range map[string][]string{
		"no equals":    {"gouroboros"},
		"not dir, sha": {"gouroboros=/no/such/dir"},
	} {
		if _, err := parseOverlays(with, nil); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
	if _, err := parseOverlays(nil, []string{"plutigo=x"}); err == nil {
		t.Error("fork without overlay: want error")
	}
}
