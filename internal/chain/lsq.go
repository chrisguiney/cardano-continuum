package chain

import (
	"context"
	"slices"

	"github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/gouroboros/protocol/localstatequery"
)

// What gouroboros v0.220.1's local-state-query offers for the engine's
// chain data, and what it lacks:
//
//   - Stake snapshots: GetStakeSnapshots gives each pool's stake in mark, set
//     and go, and the three totals. Nothing gives stake per credential, so
//     the per-wallet schedules the stake-floor guard needs come from the
//     View's fold of blocks, and these queries serve as a per-pool
//     cross-check.
//   - Pools: GetPoolState gives registered pools and the retiring list.
//   - Governance: GetProposals (with votes), GetDRepState and
//     GetFilteredVoteDelegatees. Each is its own query, so a Governance call
//     is not one atomic ledger state: a block can land between them.
//   - Committee: GetCommitteeMembersState. On the genesis devnet (node
//     11.1.3) the reply does not decode into CommitteeMembersStateResult,
//     so Committee is a separate call whose failure leaves Governance
//     usable.
//   - Delegation: GetFilteredDelegationsAndRewardAccounts, for the
//     credentials asked about only.

// PoolStake is a pool's stake in the node's three snapshots.
type PoolStake struct{ Mark, Set, Go uint64 }

// StakeSnapshots is the node's mark, set and go snapshots per pool, with
// each snapshot's total.
type StakeSnapshots struct {
	Pools         map[common.Blake2b224]PoolStake
	Mark, Set, Go uint64
}

// PoolRegs is the node's pool registrations.
type PoolRegs struct {
	// Registered lists the pool key hashes, sorted.
	Registered []common.Blake2b224
	// Retiring maps a pool to the epoch it retires at.
	Retiring map[common.Blake2b224]uint64
}

// NodeProposal is an open governance action as the node reports it, with
// the votes cast on it by credential hash.
type NodeProposal struct {
	ID                       GovID
	ProposedIn, ExpiresAfter uint64
	DRepVotes, SPOVotes      map[common.Blake2b224]uint8
	CommitteeVotes           map[common.Blake2b224]uint8
}

// DRepInfo is a registered DRep: its expiry epoch and the stake
// credentials delegating to it.
type DRepInfo struct {
	Expiry     uint64
	Delegators []common.Blake2b224
}

// Member is a constitutional committee member by cold credential hash. Hot
// is the authorized hot credential, nil when none is.
type Member struct {
	Hot      *common.Blake2b224
	Resigned bool
	Expiry   *uint64
}

// Gov is the node's proposals, DReps and vote delegations.
type Gov struct {
	Proposals       []NodeProposal
	DReps           map[common.Blake2b224]DRepInfo
	VoteDelegations map[common.Blake2b224]DRep
}

// Delegation is a credential's pool and reward balance.
type Delegation struct {
	Pool   common.Blake2b224
	Reward uint64
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

func stakeSnapshotsFrom(r *localstatequery.StakeSnapshotsResult) StakeSnapshots {
	out := StakeSnapshots{
		Pools: map[common.Blake2b224]PoolStake{},
		Mark:  r.TotalStakeMark, Set: r.TotalStakeSet, Go: r.TotalStakeGo,
	}
	for id, s := range r.PoolSnapshots {
		if s != nil {
			out.Pools[id] = PoolStake{Mark: s.StakeMark, Set: s.StakeSet, Go: s.StakeGo}
		}
	}
	return out
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

func poolRegsFrom(r *localstatequery.PoolStateResult) PoolRegs {
	out := PoolRegs{Retiring: map[common.Blake2b224]uint64{}}
	for id := range r.PState {
		out.Registered = append(out.Registered, id)
	}
	slices.SortFunc(out.Registered, compareHash)
	for id, e := range r.Retiring {
		out.Retiring[id] = e
	}
	return out
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

func govFrom(
	props localstatequery.ProposalsResult,
	dreps localstatequery.DRepStateResult,
	votes localstatequery.FilteredVoteDelegateesResult,
) Gov {
	out := Gov{
		DReps:           map[common.Blake2b224]DRepInfo{},
		VoteDelegations: map[common.Blake2b224]DRep{},
	}
	for _, p := range props {
		out.Proposals = append(out.Proposals, NodeProposal{
			ID:         GovID{Tx: p.Id.TransactionId, Index: p.Id.GovActionIdx},
			ProposedIn: p.ProposedIn, ExpiresAfter: p.ExpiresAfter,
			DRepVotes:      credVotes(p.DRepVotes),
			SPOVotes:       hashVotes(p.SPOVotes),
			CommitteeVotes: credVotes(p.CommitteeVotes),
		})
	}
	slices.SortFunc(out.Proposals, func(a, b NodeProposal) int { return a.ID.compare(b.ID) })
	for c, d := range dreps {
		info := DRepInfo{Expiry: d.Expiry}
		for _, x := range d.Delegators {
			info.Delegators = append(info.Delegators, x.Bytes)
		}
		slices.SortFunc(info.Delegators, compareHash)
		out.DReps[c.Bytes] = info
	}
	for c, d := range votes {
		x := DRep{Kind: d.Type}
		copy(x.Cred[:], d.Credential)
		out.VoteDelegations[c.Bytes] = x
	}
	return out
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

func committeeFrom(r *localstatequery.CommitteeMembersStateResult) map[common.Blake2b224]Member {
	out := map[common.Blake2b224]Member{}
	for c, m := range r.Members {
		mem := Member{Expiry: m.Expiry}
		switch m.HotCredStatus.Status {
		case localstatequery.HotCredAuthorized:
			if h := m.HotCredStatus.Credential; h != nil {
				hot := h.Credential
				mem.Hot = &hot
			}
		case localstatequery.HotCredResigned:
			mem.Resigned = true
		}
		out[c.Bytes] = mem
	}
	return out
}

func credVotes(m map[localstatequery.StakeCredential]common.Vote) map[common.Blake2b224]uint8 {
	out := map[common.Blake2b224]uint8{}
	for c, v := range m {
		out[c.Bytes] = uint8(v)
	}
	return out
}

func hashVotes(m map[common.Blake2b224]common.Vote) map[common.Blake2b224]uint8 {
	out := map[common.Blake2b224]uint8{}
	for c, v := range m {
		out[c] = uint8(v)
	}
	return out
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

func delegationsFrom(r *localstatequery.FilteredDelegationsAndRewardAccountsResult) map[common.Blake2b224]Delegation {
	out := map[common.Blake2b224]Delegation{}
	for c, p := range r.Delegations {
		d := out[c.Bytes]
		d.Pool = p
		out[c.Bytes] = d
	}
	for c, n := range r.Rewards {
		d := out[c.Bytes]
		d.Reward = n
		out[c.Bytes] = d
	}
	return out
}
