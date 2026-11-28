// Package contract is the registry of the Aiken validators under
// contracts/: their CIP-57 blueprint (plutus.json), the Go registry
// generated from it, parameter application, the datums and redeemers the
// templates take, and the record of one script run.
//
// The blueprint is compiled by the pinned Aiken (contracts/tools.lock) and
// committed; registry_gen.go is generated from it (make contracts), so a
// Go build needs no Aiken. Tests fail when the two drift apart and when a
// blueprint hash differs from gouroboros' hash of the same bytes.
//
//	contracts/templates/*.ak --aiken build--> plutus.json --go generate--> registry_gen.go
//	contracts/templates/*.ak ------------------go generate--> sources_gen.go
//
// sources_gen.go snapshots the Aiken sources for the UI, and Pipe renders a
// script's UPLC, flat and wrapped bytes from the committed code alone.
//
// CIP-57: https://cips.cardano.org/cip/CIP-0057 (Plutus contract blueprint).
package contract
