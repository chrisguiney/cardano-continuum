package httpapi

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/chrisguiney/cardano-continuum/internal/chain"
	"github.com/chrisguiney/cardano-continuum/internal/mistake"
	"github.com/chrisguiney/cardano-continuum/internal/oracle"
	"github.com/chrisguiney/cardano-continuum/internal/policy"
	"github.com/chrisguiney/cardano-continuum/internal/submit"
)

func oracleNode(impl, pool string) oracle.Node {
	return oracle.Node{Impl: impl, Pool: pool, Digest: "sha256:" + impl, Revisions: map[string]string{impl: "abc123"}}
}

// The adapter serialises to the API's Finding shape: the expected verdict
// is one of the sources, and every node answer carries its build.
func TestFindingsPageGolden(t *testing.T) {
	run := oracle.Run{Seed: 7, Log: "out/events.log"}
	g, err := policy.New(policy.Config{Floor: 50, Margin: 1})
	if err != nil {
		t.Fatal(err)
	}
	s := oracle.NewSet()
	dirty := oracleNode("dingo", "p2")
	dirty.Dirty = true
	dirty.Revisions = map[string]string{"dingo": "abc123", "gouroboros": "def456"}
	want := mistake.Expect(mistake.Of(mistake.FeeTooSmall), false)
	fee := submit.Outcome{Verdict: submit.Rejected, Failures: []submit.Failure{{Class: submit.FeeTooSmall, Detail: "text of FeeTooSmallUTxO"}}}
	a := oracle.Attempt{
		Seq: 42, Tx: []byte{0x84, 0xa0}, ID: chain.TxID(bytes.Repeat([]byte{0xab}, 32)), Expected: &want,
		Answers: []oracle.Answer{
			{Node: oracleNode("cardano-node", "p1"), Outcome: fee},
			{Node: dirty, Outcome: submit.Outcome{Verdict: submit.Accepted}},
		},
	}
	s.Add(oracle.Judge(a, run)...)
	split, _ := oracle.Differential(a, run)
	s.Add(split)
	v := &oracle.Value{Genesis: 1000, Utxo: 900, Fees: 40, Deposits: 30, Rewards: 10, Treasury: 15, Reserves: 8}
	s.Add(oracle.Monitors(3, g)[0].Check(oracle.Snapshot{Value: v, Seq: 50, Run: run})...)

	page := FindingsPage(s, Page{Limit: 2, Offset: 1})
	if page.Total != 3 || len(page.Rows) != 2 {
		t.Fatalf("page = %+v", page)
	}
	got, err := json.MarshalIndent(page, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	golden := filepath.Join("testdata", "oracle_findings.golden.json")
	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.WriteFile(golden, append(got, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	wantJSON, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if string(wantJSON) != string(got)+"\n" {
		t.Fatalf("page differs from %s:\n%s", golden, got)
	}
	if empty := FindingsPage(oracle.NewSet(), Page{Limit: 10}); empty.Rows == nil || empty.Total != 0 {
		t.Fatalf("empty page %+v: rows must serialise as []", empty)
	}
	if past := FindingsPage(s, Page{Limit: 1, Offset: 10}); len(past.Rows) != 0 || past.Total != 3 {
		t.Fatalf("offset past the end: %+v", past)
	}
}
