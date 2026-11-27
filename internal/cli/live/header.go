package live

import (
	"context"
	"errors"
	"os"

	"github.com/chrisguiney/cardano-continuum/internal/chain"
	"github.com/chrisguiney/cardano-continuum/internal/cli/internal/simflags"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
)

// Header derives the header a live run on the node at lf.Node would write.
func Header(ctx context.Context, sf simflags.Flags, lf Flags) (eventlog.HeaderBody, error) {
	if lf.Node == "" || lf.Genesis == "" {
		return eventlog.HeaderBody{}, errors.New("replay --live needs --node and --genesis to fingerprint the devnet")
	}
	seed, err := readSeed(os.Getenv(seedEnv))
	if err != nil {
		return eventlog.HeaderBody{}, err
	}
	g, err := readGenesis(lf.Genesis)
	if err != nil {
		return eventlog.HeaderBody{}, err
	}
	q := chain.NewQuerier(chain.Node{Network: lf.Network, Address: lf.Node, Magic: g.Magic})
	np, err := queryParams(ctx, q)
	if err != nil {
		return eventlog.HeaderBody{}, err
	}
	cfg, err := liveConfig(sf, lf, g, np, seed)
	if err != nil {
		return eventlog.HeaderBody{}, err
	}
	return cfg.Header()
}
