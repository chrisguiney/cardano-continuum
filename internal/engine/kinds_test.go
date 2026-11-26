package engine

import (
	"slices"
	"testing"
)

func TestActorKindsCoverRefinements(t *testing.T) {
	got := ActorKinds()
	for _, k := range []string{"adversary", "proposer", "wallet", "pool", "drep", "contract", "faucet"} {
		if !slices.Contains(got, k) {
			t.Errorf("ActorKinds() lacks %q: %v", k, got)
		}
	}
}
