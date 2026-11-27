package chain

import (
	"slices"
	"testing"

	"github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/gouroboros/ledger/conway"

	"github.com/chrisguiney/cardano-continuum/internal/agenda"
)

// Stake pools may vote on no-confidence, committee updates, hard forks,
// info actions and parameter changes that touch the security group, and on
// nothing else (cardano-ledger Governance/Internal.hs,
// votingStakePoolThresholdInternal).
func TestActionOfPoolEligibility(t *testing.T) {
	size, deposit := uint(16384), uint(2_000_000)
	tests := []struct {
		name   string
		action common.GovAction
		want   string
		pools  bool
	}{
		{"info", &common.InfoGovAction{}, "info", true},
		{"no confidence", &common.NoConfidenceGovAction{}, "no_confidence", true},
		{"update committee", &common.UpdateCommitteeGovAction{}, "update_committee", true},
		{"hard fork", &common.HardForkInitiationGovAction{}, "hard_fork_initiation", true},
		{"security parameter", &conway.ConwayParameterChangeGovAction{ParamUpdate: conway.ConwayProtocolParameterUpdate{MaxTxSize: &size}}, "parameter_change", true},
		{"other parameter", &conway.ConwayParameterChangeGovAction{ParamUpdate: conway.ConwayProtocolParameterUpdate{KeyDeposit: &deposit}}, "parameter_change", false},
		{"treasury", &common.TreasuryWithdrawalGovAction{}, "treasury_withdrawals", false},
		{"constitution", &common.NewConstitutionGovAction{}, "new_constitution", false},
		{"unknown", nil, "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			name, pools := actionOf(tc.action)
			if name != tc.want || pools != tc.pools {
				t.Errorf("actionOf = %q, %t; want %q, %t", name, pools, tc.want, tc.pools)
			}
		})
	}
}

// OpenProposals carries each proposal's type and whether pools may vote on
// it, and the view tracks a pool's vote.
func TestOpenProposalsTypeAndPoolVotes(t *testing.T) {
	v, chain := delegated(t, 10)
	v.cfg.GovLifetime = 2
	info, treasury := GovID{Tx: txid(70)}, GovID{Tx: txid(70), Index: 1}
	b2 := nextAt(&chain[0], 101, 2, Tx{Hash: txid(70), Proposals: []Proposal{
		{ID: info, Type: "info", Pools: true, Deposit: 100},
		{ID: treasury, Type: "treasury_withdrawals", Deposit: 100},
	}, Votes: []Vote{{Voter: Voter{Kind: common.VoterTypeStakingPoolKeyHash, Hash: hash224(9)}, Action: info, Choice: common.GovVoteYes}}})
	mustApply(t, v, b2)
	got := v.OpenProposals()
	if len(got) != 2 || got[0].Type != "info" || !got[0].Pools || got[1].Type != "treasury_withdrawals" || got[1].Pools {
		t.Fatalf("open %+v", got)
	}
	if !v.Voted(9, agenda.ProposalID(info.String())) {
		t.Error("pool actor 9's vote not tracked")
	}
}

// A DRep's voting power is the stake delegated to it, a pool's the stake
// delegated to the pool; an undelegated credential gives none.
func TestVotingPower(t *testing.T) {
	v, chain := delegated(t, 10)
	drep, pool := Voter{Kind: common.VoterTypeDRepKeyHash, Hash: hash224(8)}, Voter{Kind: common.VoterTypeStakingPoolKeyHash, Hash: hash224(9)}
	if p := v.VotingPower(drep); p != 0 {
		t.Fatalf("DRep power %d before any delegation", p)
	}
	d := hash224(8)
	b2 := nextAt(&chain[0], 2, 2,
		certTx(70, &common.RegistrationDrepCertificate{DrepCredential: credOf(8)}),
		certTx(71, &common.VoteDelegationCertificate{
			StakeCredential: credOf(2), Drep: common.Drep{Type: common.DrepTypeAddrKeyHash, Credential: d[:]},
		}))
	mustApply(t, v, b2)
	if p := v.VotingPower(drep); p != 3000 {
		t.Errorf("DRep power %d, want actor 2's 3000", p)
	}
	if p := v.VotingPower(pool); p != 5000 {
		t.Errorf("pool power %d, want actor 1's 5000", p)
	}
	if p := v.VotingPower(Voter{Kind: common.VoterTypeConstitutionalCommitteeHotKeyHash}); p != 0 {
		t.Errorf("committee power %d, want 0: one member, one vote", p)
	}
}

// ActionTypes names every type actionOf can return, sorted.
func TestActionTypes(t *testing.T) {
	want := []string{"hard_fork_initiation", "info", "new_constitution", "no_confidence", "parameter_change", "treasury_withdrawals", "update_committee"}
	if got := ActionTypes(); !slices.Equal(got, want) {
		t.Fatalf("ActionTypes() = %v, want %v", got, want)
	}
}

func TestMayVote(t *testing.T) {
	for _, tt := range []struct {
		typ                string
		pools              bool
		drep, spo, members bool
	}{
		{"info", true, true, true, true},
		{"no_confidence", true, true, true, false},
		{"update_committee", true, true, true, false},
		{"new_constitution", false, true, false, true},
		{"hard_fork_initiation", true, true, true, true},
		{"treasury_withdrawals", false, true, false, true},
		{"parameter_change", false, true, false, true},
		{"parameter_change", true, true, true, true},
		{"", false, false, false, false},
	} {
		d, s, m := MayVote(tt.typ, tt.pools)
		if d != tt.drep || s != tt.spo || m != tt.members {
			t.Errorf("%s pools=%v: drep %v spo %v committee %v, want %v %v %v", tt.typ, tt.pools, d, s, m, tt.drep, tt.spo, tt.members)
		}
	}
}
