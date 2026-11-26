package mistake

import (
	"errors"
	"testing"

	"github.com/blinklabs-io/gouroboros/ledger/conway"

	"github.com/chrisguiney/cardano-continuum/internal/build"
	"github.com/chrisguiney/cardano-continuum/internal/fakechain"
	"github.com/chrisguiney/cardano-continuum/internal/genesis"
	"github.com/chrisguiney/cardano-continuum/internal/submit"
)

// TestOverDeclaredRejectedThenFixed: declaring more than maxTxExUnits is a
// phase-1 ExUnitsTooBigUTxO (cardano-ledger Alonzo/Rules/Utxo.hs
// validateExUnitsTooBigUTxO), with collateral raised to cover the larger
// fee so no other rule fails; the fix for that class drops the mistake, and
// the corrected budget is accepted.
func TestOverDeclaredRejectedThenFixed(t *testing.T) {
	wd, _ := fakeWorld(t)
	wd.prepare(t)
	b := wd.base(t, ExUnitsOverDeclared)
	tx, want, err := Inject(b.Tx, Of(ExUnitsOverDeclared), wd.ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	if u, err := build.Declared(tx); err != nil || u.Memory <= wd.p.MaxBlockExUnits.Memory {
		t.Fatalf("declared %+v (%v), want past the block's %d", u, err, wd.p.MaxBlockExUnits.Memory)
	}
	o := wd.c.send(t, tx)
	if !want.Pass(o) || !o.Has(submit.ExUnitsTooBig) || len(o.Failures) != 1 {
		t.Fatalf("expected %v, got %s", want, outcomeText(o))
	}
	if Carry(Of(ExUnitsOverDeclared), []submit.Class{submit.ExUnitsTooBig}) != 0 {
		t.Fatal("fixing ExUnitsTooBigUTxO does not drop the over-declaration")
	}
	if o := wd.c.send(t, b.Tx); o.Verdict != submit.Accepted {
		t.Fatalf("the corrected budget: %s", outcomeText(o))
	}
}

// gapParams are fake-chain parameters whose per-transaction memory
// exceeds the per-block memory.
func gapParams(t testing.TB) conway.ConwayProtocolParameters {
	t.Helper()
	s := genesis.Default()
	s.TxExUnits.Memory, s.Allow.InconsistentExUnits = 140_000_000, true
	pp, err := fakechain.ParamsFor(s)
	if err != nil {
		t.Fatal(err)
	}
	return pp
}

// TestOverDeclaredInGap: on a genesis whose per-transaction budget exceeds
// the per-block one, the same honest over-declaration passes admission and
// is never forged; the verdict admits acceptance.
func TestOverDeclaredInGap(t *testing.T) {
	wd, c := fakeWorldOn(t, gapParams(t))
	wd.prepare(t)
	b := wd.base(t, ExUnitsOverDeclared)
	tx, want, err := Inject(b.Tx, Of(ExUnitsOverDeclared), wd.ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	if !want.Accept || !want.Allows(submit.ExUnitsTooBig) {
		t.Fatalf("gap verdict %v: want accepted or ExUnitsTooBigUTxO", want)
	}
	if e := ExpectOn(Of(ExUnitsOverDeclared), false, wd.p); !e.Accept {
		t.Fatalf("ExpectOn on the gap genesis = %v", e)
	}
	if o := wd.c.send(t, tx); o.Verdict != submit.Accepted {
		t.Fatalf("gap over-declaration: %s", outcomeText(o))
	}
	id := fakechain.TxID(txID(t, tx))
	if err := c.AdvanceTo(c.Slot() + 20); err != nil {
		t.Fatal(err)
	}
	for _, blk := range c.Blocks() {
		for _, x := range blk.Txs {
			if x == id {
				t.Fatal("the over-declared transaction was forged")
			}
		}
	}
	if !c.HasTx(id) {
		t.Fatal("the over-declared transaction left the mempool before it expired")
	}
	if e := ExpectOn(Of(ExUnitsOverDeclared), false, build.Params{MaxTxExUnits: wd.p.MaxBlockExUnits, MaxBlockExUnits: wd.p.MaxBlockExUnits}); e.Accept {
		t.Fatal("ExpectOn accepts the over-declaration on a consistent genesis")
	}
}

// TestUnderDeclaredWithheld: the under-declaration flags the transaction
// phase-2 invalid, so a node takes its collateral. The fake chain does not
// model collateral collection and refuses it, so the engine never samples
// the mistake.
func TestUnderDeclaredWithheld(t *testing.T) {
	if Withheld(ExUnitsUnderDeclared) == "" {
		t.Fatal("exunits_under_declared is not withheld")
	}
	if Withheld(ExUnitsOverDeclared) != "" || Withheld(FeeTooSmall) != "" {
		t.Fatal("a runnable mistake is withheld")
	}
	wd, c := fakeWorld(t)
	wd.prepare(t)
	b := wd.base(t, ExUnitsUnderDeclared)
	tx, want, err := Inject(b.Tx, Of(ExUnitsUnderDeclared), wd.ctx(t))
	if err != nil {
		t.Fatal(err)
	}
	if !want.Accept {
		t.Fatalf("verdict %v: a phase-2-invalid transaction is accepted", want)
	}
	parsed, err := conway.NewConwayTransactionFromCbor(tx)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.IsValid() {
		t.Fatal("the under-declared transaction is flagged valid")
	}
	if err := c.Submit(t.Context(), fakechain.EraConway, tx); !errors.Is(err, fakechain.ErrUnsupported) {
		t.Fatalf("fake chain answered %v; when it models collateral collection, lift the withholding", err)
	}
}
