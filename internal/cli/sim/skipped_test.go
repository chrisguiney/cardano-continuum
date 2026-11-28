package sim

import (
	"reflect"
	"testing"
)

func TestSkipReason(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"spendable 123 below 4567":  "spendable N below N",
		"actor 12 operates no pool": "actor N operates no pool",
		"nothing to burn":           "nothing to burn",
	} {
		if got := skipReason(in); got != want {
			t.Errorf("skipReason(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSkippedLineTop(t *testing.T) {
	t.Parallel()
	got := topSkips(map[string]int{"a": 1, "b": 5, "c": 5, "d": 2}, 3)
	want := []string{"b=5", "c=5", "d=2"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("topSkips = %v, want %v", got, want)
	}
}
