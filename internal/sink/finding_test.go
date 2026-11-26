package sink

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/oracle"
	"github.com/chrisguiney/cardano-continuum/internal/submit"
)

func TestExportFindings(t *testing.T) {
	verdict := oracle.Finding{
		ID: "id1", Kind: oracle.WrongClass, Severity: oracle.Warn, Seq: 2, Summary: "extra class",
		Tx: []byte{0x84, 0xa0}, TxHash: bytes.Repeat([]byte{0xab}, 32),
		Expected: &oracle.Expectation{Classes: []submit.Class{submit.BadInputs}},
		Verdicts: []oracle.Verdict{{Node: oracle.Node{Impl: "fakechain", Pool: "f"}, Verdict: "rejected",
			Classes: []submit.Class{submit.BadInputs, "MissingRedeemers"}}},
		Facts: []oracle.Fact{{Name: "impl", Value: "fakechain", Key: true}, {Name: "unexpected", Value: "MissingRedeemers"}},
		Nodes: []oracle.Node{{Impl: "fakechain", Pool: "f"}}, Seed: 7, Replay: "continuum replay --seed 7 x.log",
		Reproducible: true,
	}
	monitor := oracle.Finding{ID: "id2", Kind: oracle.Frozen, Severity: oracle.Fatal, Seq: 2, Summary: "no block",
		Facts: []oracle.Fact{{Name: "epoch", Value: "5", Key: true}}, Seed: 7}
	recs := []eventlog.Record{
		{Class: eventlog.Header, Kind: eventlog.KindHeader, Body: enc(t, eventlog.HeaderBody{
			SeedFingerprint: []byte{1}, ProfileHash: []byte{2}, GenesisHash: []byte{3}, BinaryVersion: "m3"})},
		{At: 4, Class: eventlog.Input, Kind: eventlog.KindTick},
	}
	for _, f := range []oracle.Finding{verdict, monitor} {
		r, err := f.Record(5)
		if err != nil {
			t.Fatal(err)
		}
		recs = append(recs, r)
	}
	for i := range recs {
		recs[i].Seq = uint64(i + 1)
	}
	d := t.TempDir()
	lp := filepath.Join(d, "events.log")
	writeLog(t, lp, recs)
	out := filepath.Join(d, "out")
	export(t, lp, out, Options{})
	rows := dump(t, out, tFinding)
	if len(rows) != 2 {
		t.Fatalf("finding rows = %q", rows)
	}
	want := "3|5|id1|wrong_class|warn|2|extra class|" + hex32(0xab) + "|hKA=|reject naming only BadInputsUTxO|" +
		"fakechain@f=rejected BadInputsUTxO,MissingRedeemers|impl=fakechain;unexpected=MissingRedeemers|true|7|continuum replay --seed 7 x.log|"
	if rows[0] != want {
		t.Errorf("verdict row\n got %q\nwant %q", rows[0], want)
	}
	if want := "4|5|id2|chain_frozen|fatal|2|no block|null|null|null|null|epoch=5|false|7|null|"; rows[1] != want {
		t.Errorf("monitor row\n got %q\nwant %q", rows[1], want)
	}
	if got := len(dump(t, out, tDecision)); got != 3 {
		t.Errorf("decision rows = %d, want every non-header record", got)
	}
}
