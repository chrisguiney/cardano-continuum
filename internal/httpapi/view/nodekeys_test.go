package view

import (
	"encoding/hex"
	"reflect"
	"strings"
	"testing"

	"github.com/chrisguiney/cardano-continuum/api/v1alpha1"
	"github.com/chrisguiney/cardano-continuum/internal/chain"
)

// TestNodeKeys: a forging node runs the keys of the pool its spec names by
// hex id, or else of the issuer of the last block its own keys made; a
// relay or a logical node runs none.
func TestNodeKeys(t *testing.T) {
	hexID := strings.Repeat("ab", 28)
	var forged, older chain.Mark
	forged.Hash[0], forged.Issuer[0] = 1, 0xc1
	older.Hash[0] = 2
	withLast := func(p v1alpha1.ContinuumPool, m chain.Mark) v1alpha1.ContinuumPool {
		p.Status.LastBlock.Hash = hex.EncodeToString(m.Hash[:])
		return p
	}
	ps := []v1alpha1.ContinuumPool{
		pool("hex", strings.ToUpper(hexID), v1alpha1.Dingo, v1alpha1.TierForging, 1, "x"),
		withLast(pool("tipped", "continuum-pool-1", v1alpha1.Dingo, v1alpha1.TierForging, 1, "x"), forged),
		withLast(pool("indexed", "continuum-pool-2", v1alpha1.CardanoNode, v1alpha1.TierForging, 1, "x"), older),
		pool("fresh", "continuum-pool-3", v1alpha1.Dingo, v1alpha1.TierForging, 1, "x"),
		withLast(pool("relay", "continuum-pool-1", v1alpha1.Dingo, v1alpha1.TierValidating, 1, "x"), forged),
		pool("logical", hexID, v1alpha1.Dingo, v1alpha1.TierLogical, 0, ""),
	}
	// Only "other" follows the chain holding tipped's block.
	tips := map[string][]chain.Mark{"other": {forged}}
	issuerOf := func(hash string) (string, bool) {
		if hash == hex.EncodeToString(older.Hash[:]) {
			return strings.Repeat("cd", 28), true
		}
		return "", false
	}
	want := map[string]string{
		"hex":     hexID,
		"tipped":  hex.EncodeToString(forged.Issuer[:]),
		"indexed": strings.Repeat("cd", 28),
	}
	if got := NodeKeys(ps, tips, issuerOf); !reflect.DeepEqual(got, want) {
		t.Fatalf("keys %v, want %v", got, want)
	}
	if got := NodeKeys(ps, nil, nil); len(got) != 1 || got["hex"] != hexID {
		t.Fatalf("without tips or index: %v", got)
	}
}
