package build

import (
	"encoding/hex"
	"math/big"
	"testing"

	"github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/gouroboros/ledger/conway"
	"github.com/blinklabs-io/plutigo/data"
	"github.com/blinklabs-io/plutigo/syn"

	"github.com/chrisguiney/cardano-continuum/internal/contract"
	"github.com/chrisguiney/cardano-continuum/internal/fakechain"
	"github.com/chrisguiney/cardano-continuum/internal/keys"
)

// A hand-written script's bytes are its UPLC source compiled.
func TestScriptsCompileFromSource(t *testing.T) {
	t.Parallel()
	for _, s := range []Script{Burn(3)} {
		prog, err := syn.Parse(s.Source)
		if err != nil {
			t.Fatal(err)
		}
		db, err := syn.NameToDeBruijn(prog)
		if err != nil {
			t.Fatal(err)
		}
		flat, err := syn.Encode(db)
		if err != nil {
			t.Fatal(err)
		}
		if got, want := hex.EncodeToString(s.Flat()), hex.EncodeToString(flat); got != want {
			t.Fatalf("%s: flat %s, want %s", s.Name, got, want)
		}
		if s.Hash() != common.PlutusV3Script(s.Bytes).Hash() {
			t.Fatalf("%s: hash mismatch", s.Name)
		}
	}
	if AlwaysSucceeds.Hash() == FreeMint.Hash() {
		t.Fatal("the spend validator and the mint policy share a hash")
	}
}

// checkExUnits requires the declared units to lie within the per-tx budget
// and to cover what the fake chain's evaluation measures, and the builder's
// own evaluation to agree with the fake chain's exactly.
func (h *harness) checkExUnits(got Built, utxos []common.Utxo) {
	h.t.Helper()
	tx, err := conway.NewConwayTransactionFromCbor(got.Tx)
	if err != nil {
		h.t.Fatal(err)
	}
	if tc := tx.TotalCollateral(); len(tx.Collateral()) == 0 || tc == nil || tc.Sign() == 0 || tx.CollateralReturn() == nil {
		h.t.Fatalf("script transaction lacks collateral, total collateral or a collateral return")
	}
	measured, err := h.chain.Evaluate(got.Tx)
	if err != nil {
		h.t.Fatalf("fake chain evaluation: %v", err)
	}
	ours, err := h.b.evaluate(got.Tx, utxos)
	if err != nil {
		h.t.Fatalf("builder evaluation: %v", err)
	}
	if len(measured) == 0 {
		h.t.Fatal("no redeemer evaluated")
	}
	var total common.ExUnits
	for k, v := range tx.Witnesses().Redeemers().Iter() {
		m, ok := measured[k]
		if !ok {
			h.t.Fatalf("redeemer %v not evaluated", k)
		}
		if ours[k] != m {
			h.t.Fatalf("builder measured %v, fake chain %v", ours[k], m)
		}
		if v.ExUnits.Memory < m.Memory || v.ExUnits.Steps < m.Steps {
			h.t.Fatalf("declared %v below measured %v", v.ExUnits, m)
		}
		total.Memory += v.ExUnits.Memory
		total.Steps += v.ExUnits.Steps
	}
	if max := h.pp.MaxTxExUnits; total.Memory > max.Memory || total.Steps > max.Steps {
		h.t.Fatalf("declared %v over the per-tx budget %v", total, max)
	}
}

func unitDatum() common.Datum { return common.Datum{Data: data.NewConstr(0)} }

func (h *harness) lock(amount uint64) (Built, common.Utxo) {
	h.t.Helper()
	got, err := h.b.Plutus().Lock(ctx, h.alice, Lock{Spend: h.spend(), Script: AlwaysSucceeds, Amount: amount, Datum: unitDatum()})
	if err != nil {
		h.t.Fatal(err)
	}
	h.commit(got)
	addr := AlwaysSucceeds.Address(0)
	for _, u := range h.chain.UtxosAt(addr) {
		if u.Id.Id() == got.ID {
			return got, u
		}
	}
	h.t.Fatal("locked output not found")
	return Built{}, common.Utxo{}
}

func TestScriptLockAndUnlock(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	sp := h.spend()
	req := Lock{Spend: sp, Script: AlwaysSucceeds, Amount: 5_000_000, Datum: unitDatum()}
	got, err := h.b.Plutus().Lock(ctx, h.alice, req)
	if err != nil {
		t.Fatal(err)
	}
	tx := h.check(got, sp.Inputs, want{signers: []keys.Credential{h.alice.PaymentKeyHash()}},
		func() (Built, error) { return h.b.Plutus().Lock(ctx, h.alice, req) })
	out := tx.Outputs()[0]
	if out.Address().String() != AlwaysSucceeds.Address(0).String() || out.Datum() == nil {
		t.Fatalf("lock output %s has no inline datum at the script address", out.Address())
	}
	h.commit(got)

	_, locked := h.lock(7_000_000)
	sp = h.spend()
	un := Unlock{Spend: sp, Script: AlwaysSucceeds, Locked: locked, Redeemer: unitDatum()}
	got, err = h.b.Plutus().Unlock(ctx, h.alice, un)
	if err != nil {
		t.Fatal(err)
	}
	all := append(append([]common.Utxo(nil), sp.Inputs...), locked)
	h.check(got, all, want{signers: []keys.Credential{h.alice.PaymentKeyHash()}},
		func() (Built, error) { return h.b.Plutus().Unlock(ctx, h.alice, un) })
	h.checkExUnits(got, all)

	bs, err := h.b.Plutus().unlockSpec(un, &common.ExUnits{Memory: 1, Steps: 1})
	if err != nil {
		t.Fatal(err)
	}
	bad, err := h.b.finish(ctx, h.alice, bs)
	if err != nil {
		t.Fatal(err)
	}
	h.rejects(bad.Tx, fakechain.ValidationTagMismatch)
	h.commit(got)
}

func TestMint(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	sp := h.spend()
	req := Mint{Spend: sp, Policy: FreeMint, Name: []byte("continuum"), Quantity: 1000, Redeemer: unitDatum()}
	got, err := h.b.Plutus().Mint(ctx, h.alice, req)
	if err != nil {
		t.Fatal(err)
	}
	tx := h.check(got, sp.Inputs, want{signers: []keys.Credential{h.alice.PaymentKeyHash()}},
		func() (Built, error) { return h.b.Plutus().Mint(ctx, h.alice, req) })
	h.checkExUnits(got, sp.Inputs)
	var minted *big.Int
	for _, o := range tx.Outputs() {
		if a := o.Assets(); a != nil {
			minted = a.Asset(FreeMint.Hash(), []byte("continuum"))
		}
	}
	if minted == nil || minted.Int64() != 1000 {
		t.Fatalf("minted %v, want 1000 in the change output", minted)
	}

	bs, err := h.b.Plutus().mintSpec(req, &common.ExUnits{Memory: 1, Steps: 1})
	if err != nil {
		t.Fatal(err)
	}
	bad, err := h.b.finish(ctx, h.alice, bs)
	if err != nil {
		t.Fatal(err)
	}
	h.rejects(bad.Tx, fakechain.ValidationTagMismatch)
	h.commit(got)
}

// Contract(i) is one address per deployed contract: Contract(0) is
// AlwaysSucceeds, so a run with one contract locks where it always did,
// and every other index is a template instance with its own hash that
// unlocks on the fake chain.
func TestContractsHaveTheirOwnAddresses(t *testing.T) {
	t.Parallel()
	if Contract(0).Hash() != AlwaysSucceeds.Hash() {
		t.Fatal("contract 0 is not AlwaysSucceeds")
	}
	seen := map[common.Blake2b224]uint64{FreeMint.Hash(): 1 << 40}
	for i := range uint64(50) {
		h := Contract(i).Hash()
		if j, dup := seen[h]; dup {
			t.Fatalf("contracts %d and %d share hash %x", i, j, h)
		}
		seen[h] = i
	}

	h := newHarness(t)
	if err := h.chain.AdvanceTo(10); err != nil {
		t.Fatal(err)
	}
	c := Contract(7)
	owner := h.alice.PaymentKeyHash()
	now := h.chain.Slot()
	vest := contract.Vesting{Owner: owner[:], Until: h.b.SlotTime(now)}
	got, err := h.b.Plutus().Lock(ctx, h.alice, Lock{Spend: h.spend(), Script: c, Amount: 5_000_000, Datum: common.Datum{Data: vest.Data()}})
	if err != nil {
		t.Fatal(err)
	}
	h.commit(got)
	locked := h.chain.UtxosAt(c.Address(0))
	if len(locked) != 1 {
		t.Fatalf("%d outputs at contract 7, want 1", len(locked))
	}
	sp := h.spend()
	un := Unlock{Spend: sp, Script: c, Locked: locked[0], Redeemer: unitDatum(), Signers: []common.Blake2b224{common.Blake2b224(owner)}, From: now}
	got, err = h.b.Plutus().Unlock(ctx, h.alice, un)
	if err != nil {
		t.Fatal(err)
	}
	all := append(append([]common.Utxo(nil), sp.Inputs...), locked[0])
	h.checkExUnits(got, all)
	h.commit(got)
}

// Tokens go back to the wallet in an output of their own at min-UTxO, so
// the ADA change stays pure: a script transaction takes its collateral
// from a pure-ADA input, and a wallet whose every coin carried tokens
// could never call a script again.
func TestTokensLeaveTheChangePure(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	mint := func() Built {
		t.Helper()
		got, err := h.b.Plutus().Mint(ctx, h.alice, Mint{Spend: h.spend(), Policy: FreeMint, Name: []byte("continuum"), Quantity: 1000, Redeemer: unitDatum()})
		if err != nil {
			t.Fatal(err)
		}
		h.commit(got)
		return got
	}
	mint()
	got := mint()
	tx, err := conway.NewConwayTransactionFromCbor(got.Tx)
	if err != nil {
		t.Fatal(err)
	}
	var tokenOuts, pure int
	for _, o := range tx.Outputs() {
		if a := o.Assets(); a != nil {
			tokenOuts++
			if q := a.Asset(FreeMint.Hash(), []byte("continuum")); q == nil || q.Int64() != 2000 {
				t.Fatalf("token output holds %v, want both mints' 2000", q)
			}
			if o.Amount().Uint64() > 2_000_000 {
				t.Fatalf("token output holds %d lovelace, want about its min-UTxO", o.Amount().Uint64())
			}
		} else {
			pure++
		}
	}
	if tokenOuts != 1 || pure != 1 {
		t.Fatalf("%d token outputs and %d pure outputs, want one of each", tokenOuts, pure)
	}
}
