package build

import (
	"context"
	"testing"

	"github.com/blinklabs-io/gouroboros/ledger/common"

	"github.com/chrisguiney/cardano-continuum/internal/fakechain"
	"github.com/chrisguiney/cardano-continuum/internal/keys"
)

var ctx = context.Background()

func (h *harness) registerStake() {
	h.t.Helper()
	got, err := h.b.Stake().RegisterStake(ctx, h.alice, h.spend())
	if err != nil {
		h.t.Fatal(err)
	}
	h.commit(got)
}

func TestRegisterStake(t *testing.T) {
	h := newHarness(t)
	sp := h.spend()
	got, err := h.b.Stake().RegisterStake(ctx, h.alice, sp)
	if err != nil {
		t.Fatal(err)
	}
	h.check(got, sp.Inputs, want{
		deposit: uint64(h.pp.KeyDeposit),
		signers: []keys.Credential{h.alice.PaymentKeyHash(), h.alice.StakeKeyHash()},
	}, func() (Built, error) { return h.b.Stake().RegisterStake(ctx, h.alice, sp) })

	unsigned, err := h.b.finish(ctx, h.alice, h.b.Stake().stakeSpec(sp, h.alice, nil))
	if err != nil {
		t.Fatal(err)
	}
	h.rejects(unsigned.Tx, fakechain.MissingVKeyWitnesses)

	h.commit(got)
	again, err := h.b.Stake().RegisterStake(ctx, h.alice, h.spend())
	if err != nil {
		t.Fatal(err)
	}
	h.rejects(again.Tx, fakechain.StakeKeyRegistered)
}

func TestDelegateRegistersAndDelegates(t *testing.T) {
	h := newHarness(t)
	pool := h.registerPool(9)
	sp := h.spend()
	req := Delegation{Spend: sp, Pool: pool, Register: true}
	got, err := h.b.Stake().Delegate(ctx, h.alice, req)
	if err != nil {
		t.Fatal(err)
	}
	h.check(got, sp.Inputs, want{
		deposit: uint64(h.pp.KeyDeposit),
		signers: []keys.Credential{h.alice.PaymentKeyHash(), h.alice.StakeKeyHash()},
	}, func() (Built, error) { return h.b.Stake().Delegate(ctx, h.alice, req) })
	h.commit(got)

	var unknown [28]byte
	unknown[3] = 1
	bad, err := h.b.Stake().Delegate(ctx, h.alice, Delegation{Spend: h.spend(), Pool: unknown})
	if err != nil {
		t.Fatal(err)
	}
	h.rejects(bad.Tx, fakechain.DelegateeNotRegistered)
}

func TestDeregisterStakeRefunds(t *testing.T) {
	h := newHarness(t)
	h.registerStake()
	sp := h.spend()
	got, err := h.b.Stake().DeregisterStake(ctx, h.alice, sp)
	if err != nil {
		t.Fatal(err)
	}
	h.check(got, sp.Inputs, want{
		refund:  uint64(h.pp.KeyDeposit),
		signers: []keys.Credential{h.alice.PaymentKeyHash(), h.alice.StakeKeyHash()},
	}, func() (Built, error) { return h.b.Stake().DeregisterStake(ctx, h.alice, sp) })
	h.commit(got)
	if h.chain.Deposits() != 0 {
		t.Fatalf("deposits %d after deregistration", h.chain.Deposits())
	}
}

func TestDelegateVoteThenWithdraw(t *testing.T) {
	h := newHarness(t)
	h.registerStake()

	early, err := h.b.Stake().Withdraw(ctx, h.alice, Withdrawal{Spend: h.spend()})
	if err != nil {
		t.Fatal(err)
	}
	h.rejects(early.Tx, fakechain.WdrlNotDelegatedToDRep)

	sp := h.spend()
	vd := VoteDelegation{Spend: sp, DRep: common.Drep{Type: common.DrepTypeAbstain}}
	got, err := h.b.Stake().DelegateVote(ctx, h.alice, vd)
	if err != nil {
		t.Fatal(err)
	}
	h.check(got, sp.Inputs, want{signers: []keys.Credential{h.alice.PaymentKeyHash(), h.alice.StakeKeyHash()}},
		func() (Built, error) { return h.b.Stake().DelegateVote(ctx, h.alice, vd) })
	h.commit(got)

	sp = h.spend()
	wd, err := h.b.Stake().Withdraw(ctx, h.alice, Withdrawal{Spend: sp})
	if err != nil {
		t.Fatal(err)
	}
	h.check(wd, sp.Inputs, want{signers: []keys.Credential{h.alice.PaymentKeyHash(), h.alice.StakeKeyHash()}},
		func() (Built, error) { return h.b.Stake().Withdraw(ctx, h.alice, Withdrawal{Spend: sp}) })
	h.commit(wd)

	over, err := h.b.Stake().Withdraw(ctx, h.alice, Withdrawal{Spend: h.spend(), Amount: 1})
	if err != nil {
		t.Fatal(err)
	}
	h.rejects(over.Tx, fakechain.WithdrawalsNotInRewards)
}
