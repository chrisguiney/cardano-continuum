package tla

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

const tableBugCfg = `\* SPEC: Utxo.tla
SPECIFICATION TableSpec
CONSTANTS
    Era = "Shelley"
    MinFee = 1
    MinUtxo = 1
    KeyDeposit = 1
    CollPct = 150
    MaxColl = 2
    StartUtxo <- LedgerStart
    MaxTxs = 0
    Mutation = "GoTtlExclusive"
INVARIANTS
    GoAgrees
`

func TestRewriteCfg(t *testing.T) {
	text, consts, err := utxoWrapper.rewriteCfg(tableBugCfg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "SPECIFICATION WTableSpec\n") {
		t.Errorf("specification not wrapped:\n%s", text)
	}
	if !strings.Contains(text, "INVARIANTS\n    WitGoAgrees\n    GoAgrees\n") {
		t.Errorf("witness invariant not listed first:\n%s", text)
	}
	if consts["Era"] != "Shelley" || consts["Mutation"] != "GoTtlExclusive" {
		t.Errorf("consts = %v", consts)
	}
	if _, _, err := utxoWrapper.rewriteCfg("SPECIFICATION Other\n"); err == nil {
		t.Error("unknown specification accepted")
	}
	if _, _, err := utxoWrapper.rewriteCfg("INVARIANTS\n    GoAgrees\n"); err == nil {
		t.Error("cfg without a specification accepted")
	}
}

// modelDir holds the committed copy of Utxo.tla and a Bug cfg beside it.
func modelDir(t *testing.T, cfgs map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	model, err := os.ReadFile("testdata/Utxo.tla")
	if err != nil {
		t.Fatal(err)
	}
	cfgs["Utxo.tla"] = string(model)
	for name, text := range cfgs {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// TestExportUtxoTable: the witness of the Shelley TTL mutation is the
// transaction whose TTL is the current slot, which the reference accepts.
func TestExportUtxoTable(t *testing.T) {
	tools := testTools(t)
	dir := modelDir(t, map[string]string{"UtxoGoTtlExclusiveBug.cfg": tableBugCfg})
	ex, err := ExportUtxo(context.Background(), tools, dir, "UtxoGoTtlExclusiveBug.cfg", 2*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if ex.Status != Violation || ex.Prov.Invariant != "GoAgrees" || ex.Era != "Shelley" {
		t.Fatalf("status %v invariant %q era %q: %s", ex.Status, ex.Prov.Invariant, ex.Era, ex.Detail)
	}
	if len(ex.Rows) == 0 || len(ex.Run) != 0 {
		t.Fatalf("rows %d run %d", len(ex.Rows), len(ex.Run))
	}
	for _, r := range ex.Rows {
		if r.Tx.Hi != ex.Start.Slot || !r.Accepts {
			t.Errorf("witness hi %d at slot %d accepts %v", r.Tx.Hi, ex.Start.Slot, r.Accepts)
		}
	}
}

const probeCfg = `SPECIFICATION Spec
CONSTANTS
    Era = "Mary"
    MinFee = 1
    MinUtxo = 1
    KeyDeposit = 1
    CollPct = 150
    MaxColl = 2
    StartUtxo <- LedgerStart
    MaxTxs = 3
    Mutation = "none"
INVARIANTS
    ProbeNoBurn
`

// TestExportUtxoRun: a ledger trace names each applied transaction and the
// ledger after it.
func TestExportUtxoRun(t *testing.T) {
	tools := testTools(t)
	dir := modelDir(t, map[string]string{"UtxoProbeNoBurnBug.cfg": probeCfg})
	ex, err := ExportUtxo(context.Background(), tools, dir, "UtxoProbeNoBurnBug.cfg", 2*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if ex.Status != Violation || len(ex.Run) == 0 || len(ex.Rows) != 0 {
		t.Fatalf("status %v run %d rows %d: %s", ex.Status, len(ex.Run), len(ex.Rows), ex.Detail)
	}
	last := ex.Run[len(ex.Run)-1]
	if last.Tx.Mint >= 0 || !last.Accepts || last.After == nil {
		t.Fatalf("last step = %+v", last)
	}
	if ex.Start.UTxO["a"].Coin != 3 || !ex.Start.Registered {
		t.Fatalf("start = %+v", ex.Start)
	}
}

// TestWalkDeterministic: the same seed gives the same walk, another seed
// another one.
func TestWalkDeterministic(t *testing.T) {
	tools := testTools(t)
	dir := modelDir(t, map[string]string{})
	walk := func(seed int64) UtxoExport {
		t.Helper()
		ex, err := Walk(context.Background(), tools, dir, WalkSpec{Era: "Babbage", Depth: 6, Seed: seed}, 2*time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		return ex
	}
	a, b, c := walk(7), walk(7), walk(8)
	if len(a.Run) == 0 || len(a.Run) > 6 {
		t.Fatalf("walk has %d steps", len(a.Run))
	}
	if !reflect.DeepEqual(a.Run, b.Run) {
		t.Error("same seed, different walks")
	}
	if reflect.DeepEqual(a.Run, c.Run) {
		t.Error("different seeds, same walk")
	}
	if !a.Run[0].Accepts {
		t.Error("first step of a walk is an accepted transaction")
	}
}

func TestWalkRejectsBadSpec(t *testing.T) {
	if _, err := Walk(context.Background(), Tools{}, t.TempDir(), WalkSpec{Era: "x y", Depth: 3}, time.Second); err == nil {
		t.Error("bad era accepted")
	}
	if _, err := Walk(context.Background(), Tools{}, t.TempDir(), WalkSpec{Era: "Babbage"}, time.Second); err == nil {
		t.Error("zero depth accepted")
	}
}
