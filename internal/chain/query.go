package chain

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"slices"
	"time"

	ouroboros "github.com/blinklabs-io/gouroboros"
	"github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/gouroboros/ledger/shelley"
	"github.com/blinklabs-io/gouroboros/protocol/localstatequery"
)

// Guard errors.
var (
	// ErrPublicNetwork: the network magic is mainnet, preprod, preview or
	// sanchonet. The engine floods its network with deliberate mistakes, so
	// it refuses these whatever else matches.
	ErrPublicNetwork = errors.New("chain: refusing a public network")
	// ErrGenesisMismatch: the node runs a different genesis than the run's.
	ErrGenesisMismatch = errors.New("chain: node genesis differs from the run's")
)

// publicMagics are mainnet, preprod, preview and sanchonet.
var publicMagics = []uint32{764824073, 1, 2, 4}

// Genesis is what identifies a devnet over local-state-query. The query
// returns the decoded Shelley genesis, not the file, so the file hash
// cannot be compared; these fields are the ones a rerun with another
// system start or another spec would change.
type Genesis struct {
	Magic         uint32
	Start         time.Time
	EpochLength   uint64
	SecurityParam uint64
	SlotLength    time.Duration
}

// Guard refuses a public network, then any node whose genesis differs
// from want.
func Guard(want, got Genesis) error {
	for _, g := range []Genesis{want, got} {
		if slices.Contains(publicMagics, g.Magic) {
			return fmt.Errorf("%w: magic %d", ErrPublicNetwork, g.Magic)
		}
	}
	if !want.Start.Equal(got.Start) || want.Magic != got.Magic || want.EpochLength != got.EpochLength ||
		want.SecurityParam != got.SecurityParam || want.SlotLength != got.SlotLength {
		return fmt.Errorf("%w: want %+v, node has %+v", ErrGenesisMismatch, want, got)
	}
	return nil
}

// Querier asks a node's local-state-query for what the view does not fold
// from blocks: protocol parameters, the epoch, the genesis and, for
// cross-checks, the UTxOs at given addresses. Each call opens its own
// connection: queries come at epoch boundaries, and a connection idle
// between them is shut down by the node side.
type Querier struct {
	node Node
}

// NewQuerier returns a querier for n.
func NewQuerier(n Node) *Querier { return &Querier{node: n} }

// Params returns the protocol parameters in effect.
func (q *Querier) Params(ctx context.Context) (common.ProtocolParameters, error) {
	return query(ctx, q, "protocol parameters", func(c *localstatequery.Client) (common.ProtocolParameters, error) {
		return c.GetCurrentProtocolParams()
	})
}

// Epoch returns the node's current epoch.
func (q *Querier) Epoch(ctx context.Context) (uint64, error) {
	n, err := query(ctx, q, "epoch", func(c *localstatequery.Client) (int, error) { return c.GetEpochNo() })
	return uint64(max(n, 0)), err
}

// Genesis returns the node's genesis identity.
func (q *Querier) Genesis(ctx context.Context) (Genesis, error) {
	g, err := query(ctx, q, "genesis", func(c *localstatequery.Client) (*localstatequery.GenesisConfigResult, error) {
		return c.GetGenesisConfig()
	})
	if err != nil {
		return Genesis{}, err
	}
	return Genesis{
		Magic:         uint32(g.NetworkMagic),
		Start:         systemStart(g.Start),
		EpochLength:   uint64(g.EpochLength),
		SecurityParam: uint64(g.SecurityParam),
		// The ledger encodes slotLength as a fixed-point count of
		// microseconds (NominalDiffTimeMicro).
		SlotLength: time.Duration(g.SlotLength) * time.Microsecond,
	}, nil
}

// UtxosAt returns the node's UTxOs paying addrs, sorted by reference.
func (q *Querier) UtxosAt(ctx context.Context, addrs []common.Address) ([]common.Utxo, error) {
	r, err := query(ctx, q, "utxo by address", func(c *localstatequery.Client) (*localstatequery.UTxOsResult, error) {
		return c.GetUTxOByAddress(addrs)
	})
	if err != nil {
		return nil, err
	}
	var out []common.Utxo
	for id, o := range r.Results {
		out = append(out, common.Utxo{
			Id:     shelley.ShelleyTransactionInput{TxId: id.Hash, OutputIndex: uint32(id.Idx)},
			Output: o,
		})
	}
	slices.SortFunc(out, func(a, b common.Utxo) int { return RefOf(a.Id).compare(RefOf(b.Id)) })
	return out, nil
}

// query runs one blocking gouroboros call on a fresh connection, so that
// ctx can end it: on cancellation the connection is closed, which fails
// the call.
func query[T any](ctx context.Context, q *Querier, what string, fn func(*localstatequery.Client) (T, error)) (T, error) {
	var zero T
	conn, err := q.node.connect(ctx, ouroboros.WithLocalStateQueryConfig(localstatequery.NewConfig()))
	if err != nil {
		return zero, fmt.Errorf("chain: query %s: %w", what, err)
	}
	type result struct {
		v   T
		err error
	}
	res := make(chan result, 1)
	go func() {
		v, err := fn(conn.LocalStateQuery().Client)
		res <- result{v, err}
	}()
	select {
	case r := <-res:
		if err := errors.Join(r.err, conn.Close()); err != nil {
			return zero, fmt.Errorf("chain: query %s: %w", what, err)
		}
		return r.v, nil
	case <-ctx.Done():
		err := errors.Join(ctx.Err(), conn.Close())
		<-res // the call fails once the connection is closed
		return zero, fmt.Errorf("chain: query %s: %w", what, err)
	}
}

// systemStart converts the ledger's (year, day of year, picoseconds of
// day) encoding of a UTCTime.
func systemStart(s localstatequery.SystemStartResult) time.Time {
	day := time.Date(int(s.Year.Int64()), 1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, int(s.Day)-1)
	ns := new(big.Int).Quo(&s.Picoseconds, big.NewInt(1000))
	return day.Add(time.Duration(ns.Int64()))
}
