package build

import (
	"testing"

	"github.com/blinklabs-io/gouroboros/ledger/common"

	"github.com/chrisguiney/cardano-continuum/internal/fakechain"
	"github.com/chrisguiney/cardano-continuum/internal/keys"
)

func TestDRepRegisterVotePropose(t *testing.T) {
	h := newHarness(t)
	drep := h.store.DRepSigner(1)
	pay := h.alice.PaymentKeyHash()

	sp := h.spend()
	reg := DRepRegistration{Spend: sp, DRep: drep}
	got, err := h.b.RegisterDRep(ctx, h.alice, reg)
	if err != nil {
		t.Fatal(err)
	}
	h.check(got, sp.Inputs, want{deposit: h.pp.DRepDeposit, signers: []keys.Credential{pay, drep.KeyHash()}},
		func() (Built, error) { return h.b.RegisterDRep(ctx, h.alice, reg) })
	h.commit(got)

	// The proposal's return account must be a registered stake credential.
	early, err := h.b.Propose(ctx, h.alice, Proposal{Spend: h.spend()})
	if err != nil {
		t.Fatal(err)
	}
	h.rejects(early.Tx, fakechain.ProposalReturnAccountMissing)
	h.registerStake()

	sp = h.spend()
	prop := Proposal{Spend: sp}
	got, err = h.b.Propose(ctx, h.alice, prop)
	if err != nil {
		t.Fatal(err)
	}
	h.check(got, sp.Inputs, want{deposit: h.pp.GovActionDeposit, signers: []keys.Credential{pay}},
		func() (Built, error) { return h.b.Propose(ctx, h.alice, prop) })
	h.commit(got)
	action := common.GovActionId{TransactionId: got.ID, GovActionIdx: 0}
	if want := uint64(h.pp.KeyDeposit) + h.pp.DRepDeposit + h.pp.GovActionDeposit; h.chain.Deposits() != want {
		t.Fatalf("deposits %d, want %d", h.chain.Deposits(), want)
	}

	sp = h.spend()
	vote := Vote{Spend: sp, Voter: drep, Role: RoleDRep, Action: action, Choice: common.GovVoteYes}
	got, err = h.b.Vote(ctx, h.alice, vote)
	if err != nil {
		t.Fatal(err)
	}
	h.check(got, sp.Inputs, want{signers: []keys.Credential{pay, drep.KeyHash()}},
		func() (Built, error) { return h.b.Vote(ctx, h.alice, vote) })
	h.commit(got)

	missing := vote
	missing.Spend = h.spend()
	missing.Action.GovActionIdx = 1
	bad, err := h.b.Vote(ctx, h.alice, missing)
	if err != nil {
		t.Fatal(err)
	}
	h.rejects(bad.Tx, fakechain.GovActionsDoNotExist)

	stranger := vote
	stranger.Spend, stranger.Voter = h.spend(), h.store.DRepSigner(2)
	bad, err = h.b.Vote(ctx, h.alice, stranger)
	if err != nil {
		t.Fatal(err)
	}
	h.rejects(bad.Tx, fakechain.VotersDoNotExist)
}

func TestCommitteeHotKeyAuthAndVote(t *testing.T) {
	h := newHarness(t)
	h.registerStake()
	prop, err := h.b.Propose(ctx, h.alice, Proposal{Spend: h.spend()})
	if err != nil {
		t.Fatal(err)
	}
	h.commit(prop)
	cold, hot := h.store.CommitteeSigners(0)
	pay := h.alice.PaymentKeyHash()

	sp := h.spend()
	auth := HotAuth{Spend: sp, Cold: cold, Hot: hot.KeyHash()}
	got, err := h.b.AuthorizeHot(ctx, h.alice, auth)
	if err != nil {
		t.Fatal(err)
	}
	h.check(got, sp.Inputs, want{signers: []keys.Credential{pay, cold.KeyHash()}},
		func() (Built, error) { return h.b.AuthorizeHot(ctx, h.alice, auth) })
	h.commit(got)

	sp = h.spend()
	vote := Vote{Spend: sp, Voter: hot, Role: RoleCommittee, Action: common.GovActionId{TransactionId: prop.ID}, Choice: common.GovVoteNo}
	got, err = h.b.Vote(ctx, h.alice, vote)
	if err != nil {
		t.Fatal(err)
	}
	h.check(got, sp.Inputs, want{signers: []keys.Credential{pay, hot.KeyHash()}},
		func() (Built, error) { return h.b.Vote(ctx, h.alice, vote) })
	h.commit(got)

	outsider, _ := h.store.CommitteeSigners(5)
	bad, err := h.b.AuthorizeHot(ctx, h.alice, HotAuth{Spend: h.spend(), Cold: outsider, Hot: hot.KeyHash()})
	if err != nil {
		t.Fatal(err)
	}
	h.rejects(bad.Tx, fakechain.CommitteeIsUnknown)
}
