// Package profiles holds the actor profile files shipped with continuum.
package profiles

import (
	_ "embed"
	"fmt"
	"slices"
)

// M1 is the wallet-only population `continuum run --fake` uses by default:
// wallets that hold and pay each other, with births, dormancy and
// abandonment.
//
//go:embed m1.yaml
var M1 []byte

// Preview is the Preview testnet population: clusters fitted from chain data
// by lab/fit.py, weighted by the cluster's share of the network. Its header
// records the window, seed and tool versions of the fit.
//
//go:embed preview.yaml
var Preview []byte

// Preprod is the Preprod testnet population, fitted like Preview.
//
//go:embed preprod.yaml
var Preprod []byte

// Fitted lists the shipped profile names that have a fit, each with a
// profiles/NAME.target.json that lab/grade.py tests a run against.
var Fitted = []string{"preview", "preprod"}

// ErrNotFitted reports a network with no shipped fit.
var ErrNotFitted = fmt.Errorf("not fitted yet")

// Named returns the shipped profile file called name.
func Named(name string) ([]byte, error) {
	switch name {
	case "m1":
		return M1, nil
	case "m3":
		return M3, nil
	case "preview":
		return Preview, nil
	case "preprod":
		return Preprod, nil
	case "mainnet":
		return nil, fmt.Errorf("mainnet profiles are %w", ErrNotFitted)
	}
	return nil, fmt.Errorf("unknown profile set %q (shipped: %v, mainnet)", name, slices.Concat([]string{"m1", "m3"}, Fitted))
}

// TargetFile is the name of a fitted profile set's target file.
func TargetFile(name string) string { return name + ".target.json" }

// M3 is the honest population of every intent kind: stakers, traders and
// citizens, DReps and pool operators. Run it with --pools at least the
// devnet's genesis pools.
//
//go:embed m3.yaml
var M3 []byte
