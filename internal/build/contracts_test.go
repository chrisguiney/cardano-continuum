package build

import (
	"slices"
	"strings"
	"testing"

	"github.com/blinklabs-io/gouroboros/ledger/common"

	"github.com/chrisguiney/cardano-continuum/internal/contract"
	"github.com/chrisguiney/cardano-continuum/internal/keys"
)

// The builder's scripts come from the Aiken registry: the same bytes and
// hash the blueprint holds.
func TestScriptsComeFromTheRegistry(t *testing.T) {
	t.Parallel()
	for name, s := range map[string]Script{"always_succeeds": AlwaysSucceeds, "free_mint": FreeMint} {
		v, ok := contract.Named(name)
		if !ok {
			t.Fatalf("registry has no %s", name)
		}
		if s.Validator != name || s.Language != 3 || s.Hash() != v.Hash {
			t.Fatalf("%s: validator %q language %d hash %s, registry %s", name, s.Validator, s.Language, s.Hash(), v.Hash)
		}
		if _, ok := s.Witness().(common.PlutusV3Script); !ok {
			t.Fatalf("%s: witness is %T, want a PlutusV3 script", name, s.Witness())
		}
	}
}

func TestContractTemplates(t *testing.T) {
	t.Parallel()
	for i, want := range []string{"always_succeeds", "vesting", "escrow", "vesting", "escrow"} {
		c := Contract(uint64(i))
		if c.Validator != want || Template(uint64(i)) != want {
			t.Fatalf("contract %d is %q (template %q), want %q", i, c.Validator, Template(uint64(i)), want)
		}
		got, ok := Lookup(c.Hash())
		if !ok || got.Name != c.Name {
			t.Fatalf("Lookup(contract %d) = %q, %v", i, got.Name, ok)
		}
	}
	if Contract(3).Name != "vesting#3" {
		t.Fatalf("contract 3 is named %q", Contract(3).Name)
	}
}

// vestingLock locks amount at contract 1 for alice, claimable from until.
func (h *harness) vestingLock(amount uint64, until int64) common.Utxo {
	h.t.Helper()
	c := Contract(1)
	owner := h.alice.PaymentKeyHash()
	d := contract.VestingDatum{Owner: owner[:], LockUntil: until}
	got, err := h.b.Plutus().Lock(ctx, h.alice, Lock{Spend: h.spend(), Script: c, Amount: amount, Datum: common.Datum{Data: d.Data()}})
	if err != nil {
		h.t.Fatal(err)
	}
	h.commit(got)
	for _, u := range h.chain.UtxosAt(c.Address(0)) {
		if u.Id.Id() == got.ID {
			return u
		}
	}
	h.t.Fatal("vesting output not found")
	return common.Utxo{}
}

func TestVestingClaim(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	if err := h.chain.AdvanceTo(10); err != nil {
		t.Fatal(err)
	}
	now := h.chain.Slot()
	locked := h.vestingLock(6_000_000, h.b.SlotTime(now))
	owner := common.Blake2b224(h.alice.PaymentKeyHash())

	early := Unlock{Spend: h.spend(), Script: Contract(1), Locked: locked, Redeemer: unitDatum(), Signers: []common.Blake2b224{owner}, From: now - 1}
	if _, err := h.b.Plutus().Unlock(ctx, h.alice, early); err == nil {
		t.Fatal("built a claim valid from before the deadline")
	}
	unsigned := Unlock{Spend: h.spend(), Script: Contract(1), Locked: locked, Redeemer: unitDatum(), From: now}
	if _, err := h.b.Plutus().Unlock(ctx, h.alice, unsigned); err == nil {
		t.Fatal("built a claim the owner does not sign")
	}

	sp := h.spend()
	claim := Unlock{Spend: sp, Script: Contract(1), Locked: locked, Redeemer: unitDatum(), Signers: []common.Blake2b224{owner}, From: now}
	got, err := h.b.Plutus().Unlock(ctx, h.alice, claim)
	if err != nil {
		t.Fatal(err)
	}
	all := append(append([]common.Utxo(nil), sp.Inputs...), locked)
	h.checkExUnits(got, all)

	runs, err := h.b.Runs(got.Tx, all)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 {
		t.Fatalf("%d runs, want 1", len(runs))
	}
	r := runs[0]
	if r.Validator != "vesting" || r.Instance != "vesting#1" || r.Purpose != contract.Spend || r.Script != Contract(1).Hash().String() {
		t.Fatalf("run %+v", r)
	}
	if !r.OK || r.Error != "" || r.Used.Mem == 0 || r.Used.Mem > r.Declared.Mem || r.Used.Steps > r.Declared.Steps {
		t.Fatalf("run %+v did not pass within its declared units", r)
	}
	if r.Datum == "" || !strings.Contains(r.DatumJSON, "constructor") || r.Redeemer == "" {
		t.Fatalf("run %+v lacks its datum or redeemer", r)
	}
	h.commit(got)
}

// escrowLock locks amount at contract 2 from alice for beneficiary,
// refundable from deadline.
func (h *harness) escrowLock(amount uint64, beneficiary keys.Wallet, deadline int64) common.Utxo {
	h.t.Helper()
	c := Contract(2)
	dep, ben := h.alice.PaymentKeyHash(), beneficiary.PaymentKeyHash()
	d := contract.EscrowDatum{Depositor: dep[:], Beneficiary: ben[:], Deadline: deadline}
	got, err := h.b.Plutus().Lock(ctx, h.alice, Lock{Spend: h.spend(), Script: c, Amount: amount, Datum: common.Datum{Data: d.Data()}})
	if err != nil {
		h.t.Fatal(err)
	}
	h.commit(got)
	for _, u := range h.chain.UtxosAt(c.Address(0)) {
		if u.Id.Id() == got.ID {
			return u
		}
	}
	h.t.Fatal("escrow output not found")
	return common.Utxo{}
}

// fund pays w amount from alice and returns w's spend.
func (h *harness) fund(w keys.Wallet, amount uint64) Spend {
	h.t.Helper()
	got, err := h.b.Pay(ctx, h.alice, Payment{Inputs: h.spend().Inputs, TTL: h.chain.Slot() + 1000, To: w.Address(), Amount: amount})
	if err != nil {
		h.t.Fatal(err)
	}
	h.commit(got)
	return Spend{Inputs: h.chain.UtxosAt(w.Address()), TTL: h.chain.Slot() + 1000}
}

// Two parties: bob claims a release alice approves by co-signing; alice
// takes back a deposit only from its deadline.
func TestEscrowTwoParties(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	bob := h.store.Wallet(2)
	bobSpend := h.fund(bob, 40_000_000)
	alice := h.alice.PaymentSigner()
	release := common.Datum{Data: contract.Release.Data()}

	locked := h.escrowLock(5_000_000, bob, h.b.SlotTime(h.chain.Slot()+1000))
	alone := Unlock{Spend: bobSpend, Script: Contract(2), Locked: locked, Redeemer: release}
	if _, err := h.b.Plutus().Unlock(ctx, bob, alone); err == nil {
		t.Fatal("built a release the depositor does not sign")
	}

	claim := Unlock{Spend: bobSpend, Script: Contract(2), Locked: locked, Redeemer: release, CoSigners: []keys.Signer{alice}}
	got, err := h.b.Plutus().Unlock(ctx, bob, claim)
	if err != nil {
		t.Fatal(err)
	}
	h.checkExUnits(got, append(append([]common.Utxo(nil), bobSpend.Inputs...), locked))
	if !slices.Contains(got.Signers, alice.KeyHash()) {
		t.Fatalf("release signers %x lack the depositor", got.Signers)
	}
	h.commit(got)

	now := h.chain.Slot()
	locked = h.escrowLock(5_000_000, bob, h.b.SlotTime(now+20))
	refund := common.Datum{Data: contract.Refund.Data()}
	me := []common.Blake2b224{common.Blake2b224(h.alice.PaymentKeyHash())}
	early := Unlock{Spend: h.spend(), Script: Contract(2), Locked: locked, Redeemer: refund, Signers: me, From: now + 19}
	if _, err := h.b.Plutus().Unlock(ctx, h.alice, early); err == nil {
		t.Fatal("built a refund before the deadline")
	}
	if err := h.chain.AdvanceTo(now + 21); err != nil {
		t.Fatal(err)
	}
	back, err := h.b.Plutus().Unlock(ctx, h.alice, Unlock{Spend: h.spend(), Script: Contract(2), Locked: locked, Redeemer: refund, Signers: me, From: now + 20})
	if err != nil {
		t.Fatal(err)
	}
	h.commit(back)
}

// A redeemer declaring fewer units than its script spends is a failed
// run, with the units it did spend.
func TestRunsReportAnUnderBudget(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	_, locked := h.lock(7_000_000)
	sp := h.spend()
	bs, err := h.b.Plutus().unlockSpec(Unlock{Spend: sp, Script: AlwaysSucceeds, Locked: locked, Redeemer: unitDatum()}, &common.ExUnits{Memory: 1, Steps: 1})
	if err != nil {
		t.Fatal(err)
	}
	bad, err := h.b.finish(ctx, h.alice, bs)
	if err != nil {
		t.Fatal(err)
	}
	runs, err := h.b.Runs(bad.Tx, append(append([]common.Utxo(nil), sp.Inputs...), locked))
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].OK || runs[0].Error == "" || runs[0].Used.Mem <= runs[0].Declared.Mem || runs[0].Validator != "always_succeeds" {
		t.Fatalf("runs %+v", runs)
	}
}
