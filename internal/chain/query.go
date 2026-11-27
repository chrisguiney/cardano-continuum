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
	ocommon "github.com/blinklabs-io/gouroboros/protocol/common"
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
	// ActiveSlots is the active slot coefficient f; zero when unknown.
	// Guard does not compare it: the query does not return it.
	ActiveSlots float64
}

// StabilityWindow is 3k/f slots: how far past its tip a node can translate
// slots to time, so the latest validity bound a script transaction may
// carry without the node answering PastHorizon. Zero when f is unknown.
func (g Genesis) StabilityWindow() uint64 {
	if g.ActiveSlots <= 0 || g.ActiveSlots > 1 {
		return 0
	}
	return uint64(3 * float64(g.SecurityParam) / g.ActiveSlots)
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
	return utxosOf(r), nil
}

// UtxosAtPoint is UtxosAt in the ledger state at point at, acquired with
// local-state-query's MsgAcquire, so the answer is comparable with a view
// taken at the same point. A node holds the states of its last k blocks
// only: an older point, or one off its chain, is an acquire failure
// (localstatequery.ErrAcquireFailurePointTooOld, ...PointNotOnChain).
func (q *Querier) UtxosAtPoint(ctx context.Context, at Point, addrs []common.Address) ([]common.Utxo, error) {
	r, err := query(ctx, q, "utxo by address at a point", func(c *localstatequery.Client) (*localstatequery.UTxOsResult, error) {
		p := ocommon.NewPoint(at.Slot, at.Hash[:])
		if err := c.Acquire(&p); err != nil {
			return nil, err
		}
		return c.GetUTxOByAddress(addrs)
	})
	if err != nil {
		return nil, err
	}
	return utxosOf(r), nil
}

func utxosOf(r *localstatequery.UTxOsResult) []common.Utxo {
	var out []common.Utxo
	for id, o := range r.Results {
		out = append(out, common.Utxo{
			Id:     shelley.ShelleyTransactionInput{TxId: id.Hash, OutputIndex: uint32(id.Idx)},
			Output: o,
		})
	}
	slices.SortFunc(out, func(a, b common.Utxo) int { return RefOf(a.Id).compare(RefOf(b.Id)) })
	return out
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

// StakeSnapshots returns the node's per-pool stake snapshots.
func (q *Querier) StakeSnapshots(ctx context.Context) (StakeSnapshots, error) {
	r, err := query(ctx, q, "stake snapshots", func(c *localstatequery.Client) (*localstatequery.StakeSnapshotsResult, error) {
		return c.GetStakeSnapshots(nil)
	})
	if err != nil {
		return StakeSnapshots{}, err
	}
	return stakeSnapshotsFrom(r), nil
}

// PoolRegistrations returns the node's registered and retiring pools.
func (q *Querier) PoolRegistrations(ctx context.Context) (PoolRegs, error) {
	r, err := query(ctx, q, "pool state", func(c *localstatequery.Client) (*localstatequery.PoolStateResult, error) {
		return c.GetPoolState(nil)
	})
	if err != nil {
		return PoolRegs{}, err
	}
	return poolRegsFrom(r), nil
}

// Governance returns the node's proposals, DReps and vote delegations, from
// three queries on one connection.
func (q *Querier) Governance(ctx context.Context) (Gov, error) {
	type raw struct {
		props localstatequery.ProposalsResult
		dreps localstatequery.DRepStateResult
		votes localstatequery.FilteredVoteDelegateesResult
	}
	r, err := query(ctx, q, "governance", func(c *localstatequery.Client) (raw, error) {
		var out raw
		p, err := c.GetProposals()
		if err != nil {
			return out, err
		}
		d, err := c.GetDRepState(nil)
		if err != nil {
			return out, err
		}
		v, err := c.GetFilteredVoteDelegatees(nil)
		if err != nil {
			return out, err
		}
		return raw{*p, *d, *v}, nil
	})
	if err != nil {
		return Gov{}, err
	}
	return govFrom(r.props, r.dreps, r.votes), nil
}

// Committee returns the node's constitutional committee by cold credential.
func (q *Querier) Committee(ctx context.Context) (map[common.Blake2b224]Member, error) {
	r, err := query(ctx, q, "committee", func(c *localstatequery.Client) (*localstatequery.CommitteeMembersStateResult, error) {
		return c.GetCommitteeMembersState(nil, nil, nil)
	})
	if err != nil {
		return nil, err
	}
	return committeeFrom(r), nil
}

// Delegations returns the node's delegation and reward balance for each
// stake credential that has a delegation or a reward account.
func (q *Querier) Delegations(ctx context.Context, creds []common.Blake2b224) (map[common.Blake2b224]Delegation, error) {
	ask := make([]localstatequery.StakeCredential, len(creds))
	for i, c := range creds {
		ask[i] = localstatequery.StakeCredential{Tag: uint64(common.CredentialTypeAddrKeyHash), Bytes: c}
	}
	r, err := query(ctx, q, "delegations", func(c *localstatequery.Client) (*localstatequery.FilteredDelegationsAndRewardAccountsResult, error) {
		return c.GetFilteredDelegationsAndRewardAccounts(ask)
	})
	if err != nil {
		return nil, err
	}
	return delegationsFrom(r), nil
}
