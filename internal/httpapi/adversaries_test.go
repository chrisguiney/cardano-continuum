package httpapi

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/chrisguiney/cardano-continuum/internal/adversary"
	"github.com/chrisguiney/cardano-continuum/internal/engine"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/oracle"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
	"github.com/chrisguiney/cardano-continuum/internal/store"
)

func attackLog(t *testing.T) []eventlog.Record {
	t.Helper()
	tx := engine.TxID(bytes.Repeat([]byte{9}, 32))
	f := oracle.Finding{ID: "bad", Kind: oracle.UnexpectedAccept, Severity: oracle.Fatal, Seq: 5, Summary: "malformed accepted", TxHash: tx[:]}
	frec, err := f.Record(9000)
	if err != nil {
		t.Fatal(err)
	}
	return append(fixture(t),
		eventlog.Record{At: 3000, Class: eventlog.Decision, Kind: engine.KindAttack, Actor: 2, Body: enc(t, engine.Attack{Intent: 70, Name: "malformed-cbor", Mutation: "bad-header", Cost: 4_000_000, Txs: []engine.TxID{tx}})},
		eventlog.Record{At: 4000, Class: eventlog.Input, Kind: eventlog.KindSubmitResult, Body: enc(t, engine.SubmitResult{Intent: 70, Verdict: engine.VerdictRejected})},
		frec)
}

func advSource(t *testing.T, control *adversary.Metrics) *LogSource {
	t.Helper()
	tl, p, h := newTail(t, writeLog(t, attackLog(t)))
	if _, err := tl.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	slots, err := sched.NewSlots(1000, 100)
	if err != nil {
		t.Fatal(err)
	}
	in := store.Identity{
		Kind:      "adversary",
		Agenda:    "profit",
		Adversary: store.Adversary{CostCeiling: 10_000_000, Budget: 100_000_000, Metric: "fatal_findings", Attacks: []string{"malformed-cbor"}},
	}
	s, _ := in.Encode()
	names := fakeNames{rows: []store.ActorRow{{ID: 2, Name: "adversary-malformed-cbor-2", Role: "active", Profile: "adversary-malformed-cbor", Identity: s}}}
	return NewLogSource(LogSourceConfig{Projection: p, Hub: h, Names: names, Slots: &slots, Control: control})
}

func TestAdversariesViewLive(t *testing.T) {
	v, err := advSource(t, nil).Adversaries(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Adversaries) != 1 || v.HasControl {
		t.Fatalf("view %+v", v)
	}
	a := v.Adversaries[0]
	if a.ID != 2 || a.Attack != "malformed-cbor" || a.Agenda != "profit" || a.Budget != 100_000_000 {
		t.Fatalf("row %+v", a)
	}
	if a.Attempted != 1 || a.Landed != 0 || a.Detected != 1 || a.Rejected != 1 || len(a.Recent) != 1 || a.Recent[0].Mutation != "bad-header" {
		t.Fatalf("counters %+v", a)
	}
	if a.Cost.Spent != 4_000_000 || a.Cost.Ceiling != 10_000_000 || len(a.Cost.ByEpoch) != 1 || a.Cost.EpochSpent != 4_000_000 {
		t.Fatalf("cost %+v", a.Cost)
	}
	if a.Metric.Name != "fatal_findings" || a.Metric.Value != 1 || a.Metric.Control != nil {
		t.Fatalf("metric %+v", a.Metric)
	}
	if len(a.Findings) != 1 || a.Findings[0].Severity != "fatal" {
		t.Fatalf("findings %+v", a.Findings)
	}
	if len(v.Impact) == 0 || v.Impact[0].Control != nil {
		t.Fatalf("impact %+v", v.Impact)
	}
}

func TestAdversariesViewWithControl(t *testing.T) {
	ctl := adversary.Metrics{HonestIntents: 10, HonestEnded: 10}
	v, err := advSource(t, &ctl).Adversaries(context.Background())
	if err != nil || !v.HasControl {
		t.Fatalf("%+v %v", v, err)
	}
	c := v.Adversaries[0].Metric.Control
	if c == nil || *c != 0 {
		t.Fatalf("control baseline %v", c)
	}
	var found bool
	for _, r := range v.Impact {
		if r.Metric == "fatal_findings" {
			found = r.Control != nil && *r.Control == 0 && r.Delta != nil && *r.Delta == 1 && r.Value == 1
		}
	}
	if !found {
		t.Fatalf("fatal_findings delta missing: %+v", v.Impact)
	}
}

func TestAdversariesEmptyRun(t *testing.T) {
	tl, p, h := newTail(t, writeLog(t, fixture(t)))
	if _, err := tl.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	v, err := NewLogSource(LogSourceConfig{Projection: p, Hub: h}).Adversaries(context.Background())
	if err != nil || v.Adversaries == nil || len(v.Adversaries) != 0 || v.Impact == nil {
		t.Fatalf("%+v %v", v, err)
	}
}

func TestAdversariesEndpoint(t *testing.T) {
	srv, _ := newServer(t, nil)
	code, body := get(t, srv.URL+"/api/adversaries")
	if code != 200 || !strings.Contains(string(body), `"adversaries"`) {
		t.Fatalf("%d %s", code, body)
	}
	golden(t, "adversaries", body)
}
