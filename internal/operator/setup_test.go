package operator

import (
	"maps"
	"slices"
	"testing"
)

func TestCacheOptionsNamespace(t *testing.T) {
	all, err := CacheOptions("")
	if err != nil {
		t.Fatal(err)
	}
	if all.DefaultNamespaces != nil {
		t.Fatalf("no namespace: DefaultNamespaces = %v, want every namespace", all.DefaultNamespaces)
	}

	one, err := CacheOptions("lab")
	if err != nil {
		t.Fatal(err)
	}
	if got := slices.Collect(maps.Keys(one.DefaultNamespaces)); !slices.Equal(got, []string{"lab"}) {
		t.Fatalf("DefaultNamespaces = %v, want only lab: two releases on one cluster must not reconcile each other's pools", got)
	}
	if len(one.ByObject) != len(all.ByObject) {
		t.Fatalf("a namespace dropped the label selectors: %d of %d", len(one.ByObject), len(all.ByObject))
	}
}
