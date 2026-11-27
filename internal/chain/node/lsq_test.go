package node

import (
	"reflect"
	"testing"

	"github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/gouroboros/protocol/localstatequery"

	"github.com/chrisguiney/cardano-continuum/internal/chain"
)

func TestStakeSnapshotsFrom(t *testing.T) {
	t.Parallel()
	r := &localstatequery.StakeSnapshotsResult{
		PoolSnapshots: map[common.Blake2b224]*localstatequery.PoolStakeSnapshot{
			hash224(9):  {StakeMark: 5, StakeSet: 4, StakeGo: 3},
			hash224(10): nil,
		},
		TotalStakeMark: 50, TotalStakeSet: 40, TotalStakeGo: 30,
	}
	got := stakeSnapshotsFrom(r)
	want := StakeSnapshots{
		Pools: map[common.Blake2b224]PoolStake{hash224(9): {Mark: 5, Set: 4, Go: 3}},
		Mark:  50, Set: 40, Go: 30,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestPoolRegsFrom(t *testing.T) {
	t.Parallel()
	r := &localstatequery.PoolStateResult{
		PState:   map[common.Blake2b224]*localstatequery.PoolStateParams{hash224(9): {}, hash224(3): {}},
		Retiring: map[common.Blake2b224]uint64{hash224(3): 7},
	}
	got := poolRegsFrom(r)
	want := PoolRegs{
		Registered: []common.Blake2b224{hash224(3), hash224(9)},
		Retiring:   map[common.Blake2b224]uint64{hash224(3): 7},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestGovFrom(t *testing.T) {
	t.Parallel()
	id := common.GovActionId{TransactionId: txid(5), GovActionIdx: 2}
	drep := common.Drep{Type: common.DrepTypeAddrKeyHash, Credential: hash224(8).Bytes()}
	got := govFrom(
		localstatequery.ProposalsResult{{
			Id:             id,
			DRepVotes:      map[localstatequery.StakeCredential]common.Vote{{Bytes: hash224(8)}: common.Vote(common.GovVoteYes)},
			SPOVotes:       map[common.Blake2b224]common.Vote{hash224(9): common.Vote(common.GovVoteNo)},
			ProposedIn:     3,
			ExpiresAfter:   9,
			CommitteeVotes: nil,
		}},
		localstatequery.DRepStateResult{{Bytes: hash224(8)}: {Expiry: 12, Delegators: []localstatequery.StakeCredential{{Bytes: hash224(1)}}}},
		localstatequery.FilteredVoteDelegateesResult{{Bytes: hash224(1)}: drep},
	)
	if len(got.Proposals) != 1 {
		t.Fatalf("proposals %+v", got.Proposals)
	}
	p := got.Proposals[0]
	if p.ID != (chain.GovID{Tx: txid(5), Index: 2}) || p.ExpiresAfter != 9 ||
		p.DRepVotes[hash224(8)] != common.GovVoteYes || p.SPOVotes[hash224(9)] != common.GovVoteNo {
		t.Errorf("proposal %+v", p)
	}
	if d := got.DReps[hash224(8)]; d.Expiry != 12 || !reflect.DeepEqual(d.Delegators, []common.Blake2b224{hash224(1)}) {
		t.Errorf("drep %+v", d)
	}
	if v := got.VoteDelegations[hash224(1)]; v != (chain.DRep{Kind: common.DrepTypeAddrKeyHash, Cred: hash224(8)}) {
		t.Errorf("vote delegation %+v", v)
	}
}

func TestCommitteeFrom(t *testing.T) {
	t.Parallel()
	hot := common.Credential{Credential: hash224(6)}
	exp := uint64(40)
	got := committeeFrom(&localstatequery.CommitteeMembersStateResult{
		Members: map[localstatequery.StakeCredential]localstatequery.CommitteeMemberState{
			{Bytes: hash224(4)}: {
				HotCredStatus: localstatequery.HotCredAuthStatusValue{Status: localstatequery.HotCredAuthorized, Credential: &hot},
				Expiry:        &exp,
			},
			{Bytes: hash224(5)}: {HotCredStatus: localstatequery.HotCredAuthStatusValue{Status: localstatequery.HotCredResigned}},
		},
	})
	if m := got[hash224(4)]; m.Hot == nil || *m.Hot != hash224(6) || m.Resigned || m.Expiry == nil || *m.Expiry != 40 {
		t.Errorf("member 4 %+v", m)
	}
	if m := got[hash224(5)]; m.Hot != nil || !m.Resigned {
		t.Errorf("member 5 %+v", m)
	}
}

func TestDelegationsFrom(t *testing.T) {
	t.Parallel()
	r := &localstatequery.FilteredDelegationsAndRewardAccountsResult{
		Delegations: map[localstatequery.StakeCredential]common.Blake2b224{{Bytes: hash224(1)}: hash224(9)},
		Rewards:     map[localstatequery.StakeCredential]uint64{{Bytes: hash224(1)}: 5, {Bytes: hash224(2)}: 7},
	}
	want := map[common.Blake2b224]Delegation{hash224(1): {Pool: hash224(9), Reward: 5}, hash224(2): {Reward: 7}}
	if got := delegationsFrom(r); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}
