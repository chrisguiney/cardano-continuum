package chain

import (
	"math/big"
	"slices"
	"testing"

	"github.com/blinklabs-io/gouroboros/cbor"
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

// The view tracks a committee member's vote by the hot key the book names,
// and drops one from a hot key it does not know.
func TestCommitteeVotesTracked(t *testing.T) {
	v, chain := delegated(t, 10)
	v.cfg.GovLifetime = 2
	v.cfg.Book.AddCommittee(hash224(50), 0)
	info := GovID{Tx: txid(70)}
	cc := func(b byte) Voter {
		return Voter{Kind: common.VoterTypeConstitutionalCommitteeHotKeyHash, Hash: hash224(b)}
	}
	b2 := nextAt(&chain[0], 101, 2, Tx{Hash: txid(70), Proposals: []Proposal{{ID: info, Type: "info", Pools: true, Deposit: 100}},
		Votes: []Vote{{Voter: cc(50), Action: info, Choice: common.GovVoteYes}, {Voter: cc(51), Action: info, Choice: common.GovVoteNo}}})
	mustApply(t, v, b2)
	p := agenda.ProposalID(info.String())
	if !v.CommitteeVoted(0, p) || v.CommitteeVoted(1, p) {
		t.Fatalf("committee votes on %s: member 0 %t, member 1 %t; want only member 0", p, v.CommitteeVoted(0, p), v.CommitteeVoted(1, p))
	}
	if tl := CountVotes(v, info); tl.CommitteeYes != 1 || tl.CommitteeNo != 0 {
		t.Errorf("tally %+v: want one committee yes", tl)
	}
}

// paramWorld is delegated's view with actor 2's 3000 vote-delegated to
// DRep 8, committee seat 0 (hot key 50), and a parameter change proposed
// in epoch 1 (open through epoch 2) with DRep 8's and the seat's votes
// when yes is set.
func paramWorld(t *testing.T, yes bool) (*View, Block, GovID) {
	t.Helper()
	v, blocks := delegated(t, 10)
	v.cfg.GovLifetime = 1
	v.cfg.Book.AddCommittee(hash224(50), 0)
	d := hash224(8)
	b2 := nextAt(&blocks[0], 2, 2,
		certTx(70, &common.RegistrationDrepCertificate{DrepCredential: credOf(8)}),
		certTx(71, &common.VoteDelegationCertificate{
			StakeCredential: credOf(2), Drep: common.Drep{Type: common.DrepTypeAddrKeyHash, Credential: d[:]},
		}))
	mustApply(t, v, b2)

	change := GovID{Tx: txid(72)}
	tx := Tx{Hash: txid(72), Proposals: []Proposal{{ID: change, Type: "parameter_change"}}}
	if yes {
		tx.Votes = []Vote{
			{Voter: Voter{Kind: common.VoterTypeDRepKeyHash, Hash: d}, Action: change, Choice: common.GovVoteYes},
			{Voter: Voter{Kind: common.VoterTypeConstitutionalCommitteeHotKeyHash, Hash: hash224(50)}, Action: change, Choice: common.GovVoteYes},
		}
	}

	b3 := nextAt(&b2, 101, 3, tx)
	mustApply(t, v, b3)
	return v, b3, change
}

// A parameter change the votes enact at the boundary leaves the open list
// there, though its last voting epoch has not passed: the ledger drops an
// enacted action, and a vote on it is refused with GovActionsDoNotExist
// (ParamRealVoteOnEnactedBug in models/specs/governance-change). One the
// votes do not enact stays open to its last epoch.
func TestEnactedParamChangeLeavesOpenList(t *testing.T) {
	t.Parallel()
	for _, yes := range []bool{true, false} {
		v, b3, change := paramWorld(t, yes)
		b4 := nextAt(&b3, 201, 4)
		mustApply(t, v, b4)

		open := v.OpenProposals()
		if yes && len(open) != 0 {
			t.Errorf("enacted change still open in epoch 2: %+v", open)
		}

		if !yes && (len(open) != 1 || open[0].ID != agenda.ProposalID(change.String())) {
			t.Errorf("unvoted change not open in epoch 2: %+v", open)
		}
	}
}

// Only a parameter change touching the governance group needs its 75%
// DRep threshold; the other groups' is 67%.
func TestGovGroup(t *testing.T) {
	t.Parallel()
	period, size := uint64(25), uint(4096)
	tests := []struct {
		name string
		u    conway.ConwayProtocolParameterUpdate
		want bool
	}{
		{"technical", conway.ConwayProtocolParameterUpdate{A0: &cbor.Rat{Rat: big.NewRat(1, 4)}}, false},
		{"network", conway.ConwayProtocolParameterUpdate{MaxTxSize: &size}, false},
		{"governance", conway.ConwayProtocolParameterUpdate{DRepInactivityPeriod: &period}, true},
		{"both", conway.ConwayProtocolParameterUpdate{MaxTxSize: &size, DRepInactivityPeriod: &period}, true},
	}
	for _, tc := range tests {
		if got := govGroup(tc.u); got != tc.want {
			t.Errorf("%s: governance group %t, want %t", tc.name, got, tc.want)
		}
	}
}

// The committee's quorum binds as well as the DReps': a change with every
// DRep in favour and the seat unvoted is not enacted.
func TestRatifyNeedsCommittee(t *testing.T) {
	t.Parallel()
	v, b3, change := paramWorld(t, true)
	delete(v.votes, voteKey{change, Voter{Kind: common.VoterTypeConstitutionalCommitteeHotKeyHash, Hash: hash224(50)}})
	mustApply(t, v, nextAt(&b3, 201, 4))
	if got := v.Proposals("parameter_change"); len(got) != 1 || got[0].Enacted {
		t.Fatalf("enacted without the committee: %+v", got)
	}
}

// Enactment is part of the folded state: a rollback across the boundary
// takes it back, and a view started from a snapshot keeps it and its root,
// so a revert naming the change is enacted after a restart.
func TestEnactmentRollsBackAndRestarts(t *testing.T) {
	t.Parallel()
	v, b3, change := paramWorld(t, true)
	b4 := nextAt(&b3, 201, 4)
	mustApply(t, v, b4)
	if _, err := v.Rollback(b3.Point); err != nil {
		t.Fatal(err)
	}

	if got := v.Proposals("parameter_change"); got[0].Enacted || v.root != nil {
		t.Fatalf("rollback kept the enactment: %+v, root %v", got, v.root)
	}

	mustApply(t, v, b4)
	r, err := New(v.cfg, v.Snapshot())
	if err != nil {
		t.Fatal(err)
	}

	if r.root == nil || *r.root != change {
		t.Fatalf("restarted root %v, want %s", r.root, change)
	}

	revert := GovID{Tx: txid(73)}
	d := hash224(8)
	b5 := nextAt(&b4, 202, 5, Tx{Hash: txid(73), Proposals: []Proposal{{ID: revert, Type: "parameter_change", Prev: &change}},
		Votes: []Vote{
			{Voter: Voter{Kind: common.VoterTypeDRepKeyHash, Hash: d}, Action: revert, Choice: common.GovVoteYes},
			{Voter: Voter{Kind: common.VoterTypeConstitutionalCommitteeHotKeyHash, Hash: hash224(50)}, Action: revert, Choice: common.GovVoteYes},
		}})
	mustApply(t, r, b5)
	mustApply(t, r, nextAt(&b5, 301, 6))
	if r.root == nil || *r.root != revert {
		t.Fatalf("root %v after the revert's boundary, want %s", r.root, revert)
	}
}

// A parameter change carries its predecessor and group into the view;
// other actions carry neither.
func TestChangeOf(t *testing.T) {
	t.Parallel()
	period := uint64(25)
	parent := common.GovActionId{TransactionId: txid(5), GovActionIdx: 2}
	prev, gov := changeOf(&conway.ConwayParameterChangeGovAction{ActionId: &parent, ParamUpdate: conway.ConwayProtocolParameterUpdate{DRepInactivityPeriod: &period}})
	if prev == nil || *prev != (GovID{Tx: txid(5), Index: 2}) || !gov {
		t.Fatalf("change: prev %v, governance group %t", prev, gov)
	}

	if prev, gov := changeOf(&common.InfoGovAction{}); prev != nil || gov {
		t.Fatalf("info: prev %v, governance group %t", prev, gov)
	}
}

// The view records a seat's hot key as authorised once a block carries its
// authorisation, forgets it on a rollback past that block, keeps it in the
// state a restart resumes from, and ignores a hot key the book does not
// name.
func TestCommitteeHotKeyAuthorised(t *testing.T) {
	v, chain := delegated(t, 10)
	v.cfg.Book.AddCommittee(hash224(50), 0)
	v.cfg.Book.AddCommittee(hash224(51), 1)
	if v.HotAuthorized(0) {
		t.Fatal("seat 0 authorised before any certificate")
	}
	auth := func(cold, hot byte) common.Certificate {
		return &common.AuthCommitteeHotCertificate{ColdCredential: credOf(cold), HotCredential: credOf(hot)}
	}
	b2 := nextAt(&chain[0], 2, 2, certTx(70, auth(40, 50), auth(42, 52)))
	mustApply(t, v, b2)
	if !v.HotAuthorized(0) || v.HotAuthorized(1) {
		t.Fatalf("seat 0 %t, seat 1 %t; want only seat 0", v.HotAuthorized(0), v.HotAuthorized(1))
	}
	if got := v.Snapshot().Hot; !slices.Equal(got, []common.Blake2b224{hash224(50)}) {
		t.Errorf("snapshot hot keys %v, want only 50", got)
	}

	resumed, err := New(v.cfg, v.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	if !resumed.HotAuthorized(0) {
		t.Error("a view resumed from the snapshot lost seat 0's authorisation")
	}

	if _, err := v.Rollback(chain[0].Point); err != nil {
		t.Fatal(err)
	}
	if v.HotAuthorized(0) {
		t.Error("seat 0 still authorised after rolling the certificate back")
	}
}

// A treasury withdrawal the committee and the DReps accept is enacted at
// the boundary that ratifies it and paid at the next, as the fake chain
// does (internal/fakechain TestTreasuryWithdrawalEnactsThenPays): actor
// 2's registered account is credited, unregistered credential 3's share
// stays in the treasury, a withdrawal takes the balance, and a rollback
// and a restart keep the reckoning.
func TestTreasuryWithdrawalEnactsThenPays(t *testing.T) {
	t.Parallel()
	v, blocks := delegated(t, 10)
	v.cfg.GovLifetime = 1
	v.cfg.Book.AddCommittee(hash224(50), 0)
	v.funds = 1000
	d := hash224(8)
	b2 := nextAt(&blocks[0], 2, 2,
		certTx(70, &common.RegistrationDrepCertificate{DrepCredential: credOf(8)}),
		certTx(71, &common.VoteDelegationCertificate{
			StakeCredential: credOf(2), Drep: common.Drep{Type: common.DrepTypeAddrKeyHash, Credential: d[:]},
		}))
	mustApply(t, v, b2)

	id := GovID{Tx: txid(72)}
	b3 := nextAt(&b2, 101, 3, Tx{Hash: txid(72),
		Proposals: []Proposal{{ID: id, Type: "treasury_withdrawals", Withdrawals: []Withdrawal{{Cred: hash224(2), Amount: 300}, {Cred: hash224(3), Amount: 100}}}},
		Votes: []Vote{
			{Voter: Voter{Kind: common.VoterTypeDRepKeyHash, Hash: d}, Action: id, Choice: common.GovVoteYes},
			{Voter: Voter{Kind: common.VoterTypeConstitutionalCommitteeHotKeyHash, Hash: hash224(50)}, Action: id, Choice: common.GovVoteYes},
		}})
	mustApply(t, v, b3)

	b4 := nextAt(&b3, 201, 4)
	mustApply(t, v, b4)
	if ps := v.Proposals("treasury_withdrawals"); !ps[0].Enacted || v.Treasury() != 1000 || v.Rewards(hash224(2)) != 0 {
		t.Fatalf("epoch 2: enacted %v, treasury %d, rewards %d; want enacted, unpaid", ps[0].Enacted, v.Treasury(), v.Rewards(hash224(2)))
	}

	b5 := nextAt(&b4, 301, 5)
	mustApply(t, v, b5)
	if v.Treasury() != 700 || v.Rewards(hash224(2)) != 300 {
		t.Fatalf("epoch 3: treasury %d, rewards %d; want 700, 300", v.Treasury(), v.Rewards(hash224(2)))
	}

	b6 := nextAt(&b5, 302, 6, Tx{Hash: txid(73), Withdrawals: []Withdrawal{{Cred: hash224(2), Amount: 300}}})
	mustApply(t, v, b6)
	if v.Rewards(hash224(2)) != 0 {
		t.Fatalf("rewards %d after the withdrawal", v.Rewards(hash224(2)))
	}

	if _, err := v.Rollback(b4.Point); err != nil {
		t.Fatal(err)
	}
	if v.Treasury() != 1000 || v.Rewards(hash224(2)) != 0 || v.pending() != 400 {
		t.Fatalf("rolled back: treasury %d, rewards %d, pending %d", v.Treasury(), v.Rewards(hash224(2)), v.pending())
	}

	mustApply(t, v, b5)
	r, err := New(v.cfg, v.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	if r.Treasury() != 700 || r.Rewards(hash224(2)) != 300 || r.pending() != 0 {
		t.Fatalf("restarted: treasury %d, rewards %d, pending %d", r.Treasury(), r.Rewards(hash224(2)), r.pending())
	}
}
