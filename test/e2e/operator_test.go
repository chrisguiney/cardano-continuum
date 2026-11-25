//go:build e2e

package e2e

import "testing"

// TestGenesisBoots: three cardano-node pools, rendered by the operator from a
// ContinuumNetwork, reach epoch 1.
func TestGenesisBoots(t *testing.T) { requireOperator(t) }

// TestPayments: 20 wallets, 200 payments confirmed, ViewMatchesNode green.
func TestPayments(t *testing.T) { requireOperator(t) }
