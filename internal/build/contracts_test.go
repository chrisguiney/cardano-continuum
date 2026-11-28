package build

import (
	"strings"
	"testing"

	"github.com/blinklabs-io/gouroboros/ledger/common"

	"github.com/chrisguiney/cardano-continuum/internal/contract"
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

func TestEscrowReleaseAndRefund(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	pkh := h.alice.PaymentKeyHash()
	me := pkh[:]
	for _, act := range []contract.EscrowAction{contract.Release, contract.Refund} {
		c := Contract(2)
		d := contract.EscrowDatum{Depositor: me, Beneficiary: me}
		lock, err := h.b.Plutus().Lock(ctx, h.alice, Lock{Spend: h.spend(), Script: c, Amount: 5_000_000, Datum: common.Datum{Data: d.Data()}})
		if err != nil {
			t.Fatal(err)
		}
		h.commit(lock)
		locked := h.chain.UtxosAt(c.Address(0))[0]

		sp := h.spend()
		un := Unlock{Spend: sp, Script: c, Locked: locked, Redeemer: common.Datum{Data: act.Data()}, Signers: []common.Blake2b224{common.Blake2b224(me)}}
		got, err := h.b.Plutus().Unlock(ctx, h.alice, un)
		if err != nil {
			t.Fatalf("%s: %v", act, err)
		}
		h.checkExUnits(got, append(append([]common.Utxo(nil), sp.Inputs...), locked))
		h.commit(got)
	}
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
