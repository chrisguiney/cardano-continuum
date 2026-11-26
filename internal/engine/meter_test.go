package engine

import (
	"bytes"
	"testing"

	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
)

func meterRec(t *testing.T, at uint64, class eventlog.Class, kind string, actor uint64, body any) eventlog.Record {
	t.Helper()
	b, err := eventlog.Encode(body)
	if err != nil {
		t.Fatal(err)
	}
	return eventlog.Record{At: 0, Class: class, Kind: kind, Actor: eventlog.ActorID(actor), Body: b}
}

// The meter tallies each adversary's steps, cost and fate from the same
// records Measure folds, so a live projection and a finished log agree.
func TestMeterTalliesAttacksPerActor(t *testing.T) {
	m := NewMeter()
	tx1, tx2 := TxID(bytes.Repeat([]byte{1}, 32)), TxID(bytes.Repeat([]byte{2}, 32))
	recs := []eventlog.Record{
		meterRec(t, 0, eventlog.Decision, KindAttack, 7, Attack{Intent: 1, Name: "dust-flood", Cost: 3_000_000, Txs: []TxID{tx1}}),
		meterRec(t, 0, eventlog.Decision, KindAttack, 7, Attack{Intent: 2, Name: "dust-flood", Cost: 2_000_000, Txs: []TxID{tx2}}),
		meterRec(t, 0, eventlog.Input, eventlog.KindSubmitResult, 0, SubmitResult{Intent: 2, Verdict: VerdictRejected}),
		meterRec(t, 0, eventlog.Decision, KindConfirmed, 7, Confirmed{Intent: 1, ID: tx1}),
	}
	for _, r := range recs {
		if err := m.Add(r); err != nil {
			t.Fatal(err)
		}
	}
	as := m.Attacks()
	if len(as) != 1 {
		t.Fatalf("attacks %+v", as)
	}
	a := as[0]
	if a.Actor != 7 || a.Name != "dust-flood" || a.Steps != 2 || a.Cost != 5_000_000 || a.Landed != 1 || a.Rejected != 1 || a.Detected != 1 || len(a.Recent) != 2 {
		t.Fatalf("stat %+v", a)
	}
	if got := m.Metrics(); got.AttackSteps != 2 || got.AttackDetected != 1 || got.AttackLanded != 1 {
		t.Fatalf("metrics %+v", got)
	}
	if id, ok := m.AttackActor(tx2[:]); !ok || id != 7 {
		t.Fatalf("tx owner %d %v", id, ok)
	}
}
