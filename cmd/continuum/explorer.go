package main

import (
	"log/slog"

	"github.com/chrisguiney/cardano-continuum/internal/explorer"
	"github.com/chrisguiney/cardano-continuum/internal/fakechain"
	"github.com/chrisguiney/cardano-continuum/internal/httpapi"
	"github.com/chrisguiney/cardano-continuum/internal/httpapi/view"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

// defaultExplorerBlocks is how many of the newest blocks the explorer keeps
// in full (--explorer-blocks).
const defaultExplorerBlocks = 2000

// newExplorer returns the chain explorer fed from the projection's records.
// Its per-block limits are the protocol parameters the fake chain enforces,
// which a live network's genesis matches by default.
func newExplorer(slots sched.Slots, k uint64, keep int, names view.Names, kube view.Kube, tips view.Tips, log *slog.Logger) *httpapi.Explorer {
	pp := fakechain.DefaultParams()
	return httpapi.NewExplorer(httpapi.ExplorerOptions{
		Keep: keep, K: k, Slots: &slots, Names: names, Kube: kube, Tips: tips, Logger: log,
		Limits: explorer.Limits{
			MaxMem: uint64(max(pp.MaxBlockExUnits.Memory, 0)), MaxSteps: uint64(max(pp.MaxBlockExUnits.Steps, 0)), MaxBytes: uint64(pp.MaxBlockBodySize),
		},
	})
}
