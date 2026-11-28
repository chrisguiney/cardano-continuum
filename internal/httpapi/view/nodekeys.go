package view

import (
	"encoding/hex"
	"strings"

	"github.com/chrisguiney/cardano-continuum/api/v1alpha1"
	"github.com/chrisguiney/cardano-continuum/internal/chain"
)

// NodeKeys maps each forging node, by name, to the stake pool (hex pool id)
// whose keys it runs. Pure.
//
// A spec.poolId of 56 hex digits is that pool id. Any other poolId is a
// label, and the pool id is the issuer of status.lastBlock, the newest
// block the operator saw the node's own keys make: first from a followed
// chain's marks, else from issuerOf (the explorer's index; nil for none).
// A node that has not forged yet stays unresolved. Following a chain says
// nothing about who forged it, since every node follows every block.
func NodeKeys(ps []v1alpha1.ContinuumPool, tips map[string][]chain.Mark, issuerOf func(hash string) (string, bool)) map[string]string {
	byHash := map[string]string{}
	for _, marks := range tips {
		for _, m := range marks {
			if m.Issuer != ([28]byte{}) {
				byHash[hex.EncodeToString(m.Hash[:])] = hex.EncodeToString(m.Issuer[:])
			}
		}
	}

	out := map[string]string{}
	for _, p := range ps {
		if p.Spec.Tier != v1alpha1.TierForging {
			continue
		}
		if id, ok := poolHex(p.Spec.PoolID); ok {
			out[p.Name] = id
			continue
		}
		last := strings.ToLower(p.Status.LastBlock.Hash)
		if last == "" {
			continue
		}
		if id, ok := byHash[last]; ok {
			out[p.Name] = id
			continue
		}
		if issuerOf == nil {
			continue
		}
		if id, ok := issuerOf(last); ok {
			out[p.Name] = id
		}
	}
	return out
}

// poolHex is s as a lowercase hex pool id, if it is one.
func poolHex(s string) (string, bool) {
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != 28 {
		return "", false
	}
	return hex.EncodeToString(b), true
}
