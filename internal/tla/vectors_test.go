package tla

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadVectors(t *testing.T) {
	t.Parallel()
	row := `{"era":"Shelley","family":"interval","ledger":{"utxo":{"a":{"coin":3,"tok":0,"lock":"key"}},"slot":1,"registered":true,"reward":0},"tx":{"ins":["a"],"coll":[],"refs":[],"delta":0,"tdelta":0,"fee":1,"mint":0,"cert":"none","wdrl":false,"olock":"key","big":false,"lo":0,"hi":3,"rdmrs":false,"valid":true,"pass":true,"tcoll":-1},"specAccepts":true}`
	in := strings.Repeat(row+"\n", 6)
	got, err := LoadVectors(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 6 || !got[0].SpecAccepts || got[0].Tx.Hi != 3 || got[0].Ledger.UTxO["a"].Coin != 3 {
		t.Fatalf("got %d rows, first %+v", len(got), got[0])
	}
}

func TestLoadVectorsMalformedLine(t *testing.T) {
	t.Parallel()
	row := `{"era":"Shelley","family":"interval","specAccepts":true}`
	in := row + "\n" + row + "\n" + `{"era": }` + "\n" + row + "\n"
	_, err := LoadVectors(strings.NewReader(in))
	if err == nil || !strings.Contains(err.Error(), "line 3") {
		t.Fatalf("err = %v, want mention of line 3", err)
	}
	_, err = LoadVectors(strings.NewReader(`{"era":"Shelley","bogus":1}` + "\n"))
	if err == nil || !strings.Contains(err.Error(), "line 1") {
		t.Fatalf("unknown field: err = %v", err)
	}
}

func utxoDir(t *testing.T, era string) string {
	t.Helper()
	dir := t.TempDir()
	for _, n := range []string{"Utxo.tla", "Utxo" + era + "Table.cfg"} {
		b, err := os.ReadFile(filepath.Join("testdata", n))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, n), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func exportEra(t *testing.T, era string) VectorSet {
	t.Helper()
	tools := testTools(t)
	set, err := ExportEra(context.Background(), tools, utxoDir(t, era), era, 2*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return set
}

// TestVectorExportCount cross-checks the exported rows against TLC's own
// distinct-state count and its set cardinalities: every distinct state of
// TableSpec is dumped once, and contributes exactly the transactions TLC
// counted for each family.
func TestVectorExportCount(t *testing.T) {
	t.Parallel()
	set := exportEra(t, "Shelley")
	if set.Distinct != 81 {
		t.Fatalf("distinct states = %d, want 81", set.Distinct)
	}
	if len(set.Counts) != set.Distinct {
		t.Fatalf("dumped %d states, TLC found %d distinct", len(set.Counts), set.Distinct)
	}
	want := map[string]int{}
	total := 0
	for _, c := range set.Counts {
		for fam, n := range c {
			want[fam] += n
			total += n
		}
	}
	if total == 0 || len(set.Vectors) != total {
		t.Fatalf("rows = %d, want %d", len(set.Vectors), total)
	}
	got := map[string]int{}
	for _, v := range set.Vectors {
		got[v.Family]++
	}
	for fam, n := range want {
		if got[fam] != n {
			t.Errorf("family %s: %d rows, want %d", fam, got[fam], n)
		}
	}
}

// boundary returns the SpecAccepts verdict of the interval vector with the
// given bounds, in every ledger that has slot s and a spendable input a (so
// that only the interval can reject the base transaction). All such ledgers
// must agree.
func boundary(t *testing.T, set VectorSet, slot, lo, hi int) bool {
	t.Helper()
	seen, verdict := false, false
	for _, v := range set.Vectors {
		a, ok := v.Ledger.UTxO["a"]
		if v.Family != "interval" || v.Ledger.Slot != slot || !ok || a.Lock != "key" || a.Coin < 3 ||
			v.Tx.Lo != lo || v.Tx.Hi != hi {
			continue
		}
		if seen && verdict != v.SpecAccepts {
			t.Fatalf("ledgers disagree at slot %d lo %d hi %d", slot, lo, hi)
		}
		seen, verdict = true, v.SpecAccepts
	}
	if !seen {
		t.Fatalf("no vector at slot %d lo %d hi %d", slot, lo, hi)
	}
	return verdict
}

// Shelley: SpecInterval is tx.hi >= slot, so a ttl equal to the slot is
// accepted and one below it is not.
func TestVectorExportShelleyTTL(t *testing.T) {
	t.Parallel()
	set := exportEra(t, "Shelley")
	for hi, want := range map[int]bool{1: false, 2: true, 3: true} {
		if got := boundary(t, set, 2, 0, hi); got != want {
			t.Errorf("slot 2 ttl %d: SpecAccepts = %v, want %v", hi, got, want)
		}
	}
}

// Allegra and later: lo <= slot < hi, with 0 meaning unbounded. A start equal
// to the slot is accepted, a ttl equal to the slot is not (ttl is exclusive).
func TestVectorExportAllegraInterval(t *testing.T) {
	t.Parallel()
	set := exportEra(t, "Allegra")
	cases := []struct {
		lo, hi int
		want   bool
	}{
		{0, 0, true},
		{1, 0, true},  // start = slot
		{2, 0, false}, // start = slot + 1
		{0, 1, false}, // ttl = slot
		{0, 2, true},  // ttl = slot + 1
	}
	for _, c := range cases {
		if got := boundary(t, set, 1, c.lo, c.hi); got != c.want {
			t.Errorf("slot 1 lo %d hi %d: SpecAccepts = %v, want %v", c.lo, c.hi, got, c.want)
		}
	}
}
