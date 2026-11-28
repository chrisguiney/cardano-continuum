package build

import (
	"math/big"
	"testing"

	"github.com/blinklabs-io/gouroboros/cbor"
	"github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/gouroboros/ledger/conway"

	"github.com/chrisguiney/cardano-continuum/internal/fakechain"
	"github.com/chrisguiney/cardano-continuum/internal/keys"
	"github.com/chrisguiney/cardano-continuum/internal/protocol"
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

// A committee vote may carry its own hot-key authorisation, witnessed by
// the cold key. Below protocol version 11 cardano-node's mempool refuses
// it (the fake's unelected); from 11 the ledger accepts it, as GOV judges
// the vote against the state after the transaction's certificates.
func TestCommitteeVoteWithAuthorisation(t *testing.T) {
	t.Parallel()
	for _, major := range []uint{10, 11} {
		h := newHarnessWith(t, func(pp *conway.ConwayProtocolParameters) { pp.ProtocolVersion.Major = major })
		h.registerStake()
		prop, err := h.b.Gov().Propose(ctx, h.alice, Proposal{Spend: h.spend()})
		if err != nil {
			t.Fatal(err)
		}
		h.commit(prop)
		cold, hot := h.store.CommitteeSigners(0)

		sp := h.spend()
		vote := Vote{Spend: sp, Voter: hot, Auth: &cold, Role: RoleCommittee, Action: common.GovActionId{TransactionId: prop.ID}, Choice: common.GovVoteYes}
		got, err := h.b.Gov().Vote(ctx, h.alice, vote)
		if err != nil {
			t.Fatal(err)
		}

		if major < 11 {
			h.rejects(got.Tx, fakechain.MempoolFailure)
			continue
		}
		h.check(got, sp.Inputs, want{signers: []keys.Credential{h.alice.PaymentKeyHash(), hot.KeyHash(), cold.KeyHash()}},
			func() (Built, error) { return h.b.Gov().Vote(ctx, h.alice, vote) })
		h.commit(got)
	}
}

// A committee vote by a hot key the chain has not authorised is refused
// from the mempool, as a cardano-node refuses it: authorise first.
func TestCommitteeVoteBeforeAuthorisationRefused(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.registerStake()
	prop, err := h.b.Gov().Propose(ctx, h.alice, Proposal{Spend: h.spend(), Action: ActionInfo})
	if err != nil {
		t.Fatal(err)
	}
	h.commit(prop)
	_, hot := h.store.CommitteeSigners(0)
	vote := Vote{Spend: h.spend(), Voter: hot, Role: RoleCommittee, Action: common.GovActionId{TransactionId: prop.ID}, Choice: common.GovVoteYes}
	got, err := h.b.Gov().Vote(ctx, h.alice, vote)
	if err != nil {
		t.Fatal(err)
	}
	h.rejects(got.Tx, fakechain.MempoolFailure)
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
	case *common.HardForkInitiationGovAction:
		return ActionHardFork, true
	}
	return "", false
}

// A hard fork proposes the version that can follow the current one, with
// the last enacted hard fork (none on a fresh devnet) as predecessor; the
// fake chain's GOV rule accepts it. A version that cannot follow is
// refused before anything is built, as GOV-Propose's validHFAction would
// refuse it.
func TestProposeHardFork(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.registerStake()
	cur := protocol.Version{Major: h.pp.ProtocolVersion.Major, Minor: h.pp.ProtocolVersion.Minor}
	sp := h.spend()
	prop := Proposal{Spend: sp, Action: ActionHardFork, Fork: &HardFork{From: cur, To: cur.Next()}}
	got, err := h.b.Gov().Propose(ctx, h.alice, prop)
	if err != nil {
		t.Fatal(err)
	}
	tx := h.check(got, sp.Inputs, want{deposit: h.pp.GovActionDeposit, signers: []keys.Credential{h.alice.PaymentKeyHash()}},
		func() (Built, error) { return h.b.Gov().Propose(ctx, h.alice, prop) })
	hf, ok := tx.ProposalProcedures()[0].GovAction().(*common.HardForkInitiationGovAction)
	if !ok || hf.ActionId != nil || hf.ProtocolVersion.Major != cur.Major+1 || hf.ProtocolVersion.Minor != 0 {
		t.Fatalf("built %+v", tx.ProposalProcedures()[0].GovAction())
	}
	h.commit(got)

	for _, to := range []protocol.Version{{Major: cur.Major + 2}, cur} {
		bad := Proposal{Spend: h.spend(), Action: ActionHardFork, Fork: &HardFork{From: cur, To: to}}
		if _, err := h.b.Gov().Propose(ctx, h.alice, bad); err == nil {
			t.Errorf("a hard fork from %v to %v built", cur, to)
		}
	}
	if _, err := h.b.Gov().Propose(ctx, h.alice, Proposal{Spend: h.spend(), Action: ActionHardFork}); err == nil {
		t.Fatal("a hard fork with no version built")
	}
}

// A treasury withdrawal pays the payees it is given, each its amount;
// without any it asks for the proposer's own reward account.
func TestProposeTreasuryPayees(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.registerStake()
	bob := h.store.Wallet(2)
	payees := []Payee{{To: h.alice.StakeAddress(), Amount: 7_000_000}, {To: bob.StakeAddress(), Amount: 3_000_000}}
	got, err := h.b.Gov().Propose(ctx, h.alice, Proposal{Spend: h.spend(), Action: ActionTreasuryWithdrawals, Payees: payees})
	if err != nil {
		t.Fatal(err)
	}
	tx, err := conway.NewConwayTransactionFromCbor(got.Tx)
	if err != nil {
		t.Fatal(err)
	}
	tw, ok := tx.ProposalProcedures()[0].GovAction().(*common.TreasuryWithdrawalGovAction)
	if !ok || len(tw.Withdrawals) != 2 {
		t.Fatalf("action %+v", tx.ProposalProcedures()[0].GovAction())
	}
	paid := map[string]uint64{}
	for a, n := range tw.Withdrawals {
		paid[a.String()] = n
	}
	if paid[h.alice.StakeAddress().String()] != 7_000_000 || paid[bob.StakeAddress().String()] != 3_000_000 {
		t.Fatalf("paid %v", paid)
	}
}
