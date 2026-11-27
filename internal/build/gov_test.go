package build

import (
	"math/big"
	"testing"

	"github.com/blinklabs-io/gouroboros/cbor"
	"github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/gouroboros/ledger/conway"

	"github.com/chrisguiney/cardano-continuum/internal/fakechain"
	"github.com/chrisguiney/cardano-continuum/internal/keys"
)

func TestDRepRegisterVotePropose(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	drep := h.store.DRepSigner(1)
	pay := h.alice.PaymentKeyHash()

	sp := h.spend()
	reg := DRepRegistration{Spend: sp, DRep: drep}
	got, err := h.b.Gov().RegisterDRep(ctx, h.alice, reg)
	if err != nil {
		t.Fatal(err)
	}
	h.check(got, sp.Inputs, want{deposit: h.pp.DRepDeposit, signers: []keys.Credential{pay, drep.KeyHash()}},
		func() (Built, error) { return h.b.Gov().RegisterDRep(ctx, h.alice, reg) })
	h.commit(got)

	// The proposal's return account must be a registered stake credential.
	early, err := h.b.Gov().Propose(ctx, h.alice, Proposal{Spend: h.spend()})
	if err != nil {
		t.Fatal(err)
	}
	h.rejects(early.Tx, fakechain.ProposalReturnAccountMissing)
	h.registerStake()

	sp = h.spend()
	prop := Proposal{Spend: sp}
	got, err = h.b.Gov().Propose(ctx, h.alice, prop)
	if err != nil {
		t.Fatal(err)
	}
	h.check(got, sp.Inputs, want{deposit: h.pp.GovActionDeposit, signers: []keys.Credential{pay}},
		func() (Built, error) { return h.b.Gov().Propose(ctx, h.alice, prop) })
	h.commit(got)
	action := common.GovActionId{TransactionId: got.ID, GovActionIdx: 0}
	if want := uint64(h.pp.KeyDeposit) + h.pp.DRepDeposit + h.pp.GovActionDeposit; h.chain.Deposits() != want {
		t.Fatalf("deposits %d, want %d", h.chain.Deposits(), want)
	}

	sp = h.spend()
	vote := Vote{Spend: sp, Voter: drep, Role: RoleDRep, Action: action, Choice: common.GovVoteYes}
	got, err = h.b.Gov().Vote(ctx, h.alice, vote)
	if err != nil {
		t.Fatal(err)
	}
	h.check(got, sp.Inputs, want{signers: []keys.Credential{pay, drep.KeyHash()}},
		func() (Built, error) { return h.b.Gov().Vote(ctx, h.alice, vote) })
	h.commit(got)

	missing := vote
	missing.Spend = h.spend()
	missing.Action.GovActionIdx = 1
	bad, err := h.b.Gov().Vote(ctx, h.alice, missing)
	if err != nil {
		t.Fatal(err)
	}
	h.rejects(bad.Tx, fakechain.GovActionsDoNotExist)

	stranger := vote
	stranger.Spend, stranger.Voter = h.spend(), h.store.DRepSigner(2)
	bad, err = h.b.Gov().Vote(ctx, h.alice, stranger)
	if err != nil {
		t.Fatal(err)
	}
	h.rejects(bad.Tx, fakechain.VotersDoNotExist)
}

func TestCommitteeHotKeyAuthAndVote(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.registerStake()
	prop, err := h.b.Gov().Propose(ctx, h.alice, Proposal{Spend: h.spend()})
	if err != nil {
		t.Fatal(err)
	}
	h.commit(prop)
	cold, hot := h.store.CommitteeSigners(0)
	pay := h.alice.PaymentKeyHash()

	sp := h.spend()
	auth := HotAuth{Spend: sp, Cold: cold, Hot: hot.KeyHash()}
	got, err := h.b.Gov().AuthorizeHot(ctx, h.alice, auth)
	if err != nil {
		t.Fatal(err)
	}
	h.check(got, sp.Inputs, want{signers: []keys.Credential{pay, cold.KeyHash()}},
		func() (Built, error) { return h.b.Gov().AuthorizeHot(ctx, h.alice, auth) })
	h.commit(got)

	sp = h.spend()
	vote := Vote{Spend: sp, Voter: hot, Role: RoleCommittee, Action: common.GovActionId{TransactionId: prop.ID}, Choice: common.GovVoteNo}
	got, err = h.b.Gov().Vote(ctx, h.alice, vote)
	if err != nil {
		t.Fatal(err)
	}
	h.check(got, sp.Inputs, want{signers: []keys.Credential{pay, hot.KeyHash()}},
		func() (Built, error) { return h.b.Gov().Vote(ctx, h.alice, vote) })
	h.commit(got)

	outsider, _ := h.store.CommitteeSigners(5)
	bad, err := h.b.Gov().AuthorizeHot(ctx, h.alice, HotAuth{Spend: h.spend(), Cold: outsider, Hot: hot.KeyHash()})
	if err != nil {
		t.Fatal(err)
	}
	h.rejects(bad.Tx, fakechain.CommitteeIsUnknown)
}

func TestDRepRetirement(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	drep := h.store.DRepSigner(1)
	pay := h.alice.PaymentKeyHash()
	reg, err := h.b.Gov().RegisterDRep(ctx, h.alice, DRepRegistration{Spend: h.spend(), DRep: drep})
	if err != nil {
		t.Fatal(err)
	}
	h.commit(reg)

	sp := h.spend()
	ret := DRepRetirement{Spend: sp, DRep: drep}
	got, err := h.b.Gov().RetireDRep(ctx, h.alice, ret)
	if err != nil {
		t.Fatal(err)
	}
	h.check(got, sp.Inputs, want{refund: h.pp.DRepDeposit, signers: []keys.Credential{pay, drep.KeyHash()}},
		func() (Built, error) { return h.b.Gov().RetireDRep(ctx, h.alice, ret) })
	h.commit(got)
	if h.chain.Deposits() != 0 {
		t.Fatalf("deposits %d after the DRep retired, want 0", h.chain.Deposits())
	}

	again, err := h.b.Gov().RetireDRep(ctx, h.alice, DRepRetirement{Spend: h.spend(), DRep: drep})
	if err != nil {
		t.Fatal(err)
	}
	h.rejects(again.Tx, fakechain.DRepNotRegistered)
}

// Every action type a fitted network proposes builds a proposal the
// ledger accepts, holding the governance action deposit.
func TestProposeActionTypes(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.registerStake()
	deposits := uint64(h.pp.KeyDeposit)
	a0 := &cbor.Rat{Rat: big.NewRat(1, 4)}
	for _, a := range []ActionType{ActionInfo, ActionParameterChange, ActionTreasuryWithdrawals, ActionNoConfidence, ActionNewConstitution, ActionUpdateCommittee} {
		sp := h.spend()
		prop := Proposal{Spend: sp, Action: a}
		if a == ActionParameterChange {
			prop.Change = &ParamChange{Update: conway.ConwayProtocolParameterUpdate{A0: a0}}
		}
		got, err := h.b.Gov().Propose(ctx, h.alice, prop)
		if err != nil {
			t.Fatalf("%s: %v", a, err)
		}
		tx := h.check(got, sp.Inputs, want{deposit: h.pp.GovActionDeposit, signers: []keys.Credential{h.alice.PaymentKeyHash()}},
			func() (Built, error) { return h.b.Gov().Propose(ctx, h.alice, prop) })
		procs := tx.ProposalProcedures()
		if len(procs) != 1 {
			t.Fatalf("%s: %d proposals", a, len(procs))
		}
		if typ, ok := actionTypeOf(procs[0].GovAction()); !ok || typ != a {
			t.Fatalf("%s: built a %v action", a, procs[0].GovAction())
		}
		h.commit(got)
		deposits += h.pp.GovActionDeposit
		if h.chain.Deposits() != deposits {
			t.Fatalf("%s: deposits %d, want %d", a, h.chain.Deposits(), deposits)
		}
	}
	if _, err := h.b.Gov().Propose(ctx, h.alice, Proposal{Spend: h.spend(), Action: "no_such_action"}); err == nil {
		t.Fatal("an unknown action type built")
	}
	if _, err := h.b.Gov().Propose(ctx, h.alice, Proposal{Spend: h.spend(), Action: ActionParameterChange}); err == nil {
		t.Fatal("a parameter change with no update built")
	}
}

// A committee vote that carries its seat's hot-key authorisation is
// accepted before any separate authorisation: the certificate applies
// before the vote, and the cold and hot keys both witness it.
func TestCommitteeVoteAuthorisesItsHotKey(t *testing.T) {
	h := newHarness(t)
	h.registerStake()
	prop, err := h.b.Gov().Propose(ctx, h.alice, Proposal{Spend: h.spend(), Action: ActionInfo})
	if err != nil {
		t.Fatal(err)
	}
	h.commit(prop)
	cold, hot := h.store.CommitteeSigners(0)
	sp := h.spend()
	vote := Vote{Spend: sp, Voter: hot, Role: RoleCommittee, Cold: &cold, Action: common.GovActionId{TransactionId: prop.ID}, Choice: common.GovVoteYes}
	got, err := h.b.Gov().Vote(ctx, h.alice, vote)
	if err != nil {
		t.Fatal(err)
	}
	h.check(got, sp.Inputs, want{signers: []keys.Credential{h.alice.PaymentKeyHash(), cold.KeyHash(), hot.KeyHash()}},
		func() (Built, error) { return h.b.Gov().Vote(ctx, h.alice, vote) })
	h.commit(got)
}

func actionTypeOf(a common.GovAction) (ActionType, bool) {
	switch a.(type) {
	case *common.InfoGovAction:
		return ActionInfo, true
	case *conway.ConwayParameterChangeGovAction:
		return ActionParameterChange, true
	case *common.TreasuryWithdrawalGovAction:
		return ActionTreasuryWithdrawals, true
	case *common.NoConfidenceGovAction:
		return ActionNoConfidence, true
	case *common.NewConstitutionGovAction:
		return ActionNewConstitution, true
	case *common.UpdateCommitteeGovAction:
		return ActionUpdateCommittee, true
	}
	return "", false
}
