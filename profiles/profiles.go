// Package profiles holds the actor profile files shipped with continuum.
package profiles

import _ "embed"

// M1 is the wallet-only population `continuum run --fake` uses by default:
// wallets that hold and pay each other, with births, dormancy and
// abandonment.
//
//go:embed m1.yaml
var M1 []byte
