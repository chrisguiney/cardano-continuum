package engine

import (
	"log/slog"

	"github.com/chrisguiney/cardano-continuum/internal/chain"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/fakenode"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/live"
	"github.com/chrisguiney/cardano-continuum/internal/submit"
)

// The node adapters live in internal/engine/internal/live and fakenode.
type (
	// Live is a Node over a running cardano-node; see live.Node.
	Live = live.Node
	// FakeNode adapts a fake chain to Node and Pacer; see fakenode.Node.
	FakeNode = fakenode.Node
)

// NewLive returns a Live following d and submitting to t; see live.New.
func NewLive(d chain.Dialer, t submit.Target, k uint64, log *slog.Logger) *Live {
	return live.New(d, t, k, log)
}
