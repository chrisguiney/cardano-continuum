package engine

import (
	"context"
	"reflect"
	"testing"

	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
)

func auditLog(t testing.TB, r eventlog.Reader) Tally {
	t.Helper()
	a, err := AuditLog(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func decision(t *testing.T, kind string, body any) eventlog.Record {
	t.Helper()
	raw, err := eventlog.Encode(body)
	if err != nil {
		t.Fatal(err)
	}
	return eventlog.Record{Class: eventlog.Decision, Kind: kind, Body: raw}
}

func TestAuditCountsAndFindsDoublePayment(t *testing.T) {
	t.Parallel()
	recs := []eventlog.Record{
		decision(t, KindSubmitted, Submitted{Intent: 1}),
		decision(t, KindSubmitted, Submitted{Intent: 1}), // a rebuild is not a second payment
		decision(t, KindRefill, Refill{Intent: 2}),
		decision(t, KindConfirmed, Confirmed{Intent: 1}),
		decision(t, KindConfirmed, Confirmed{Intent: 2}),
		decision(t, KindSubmitted, Submitted{Intent: 3}),
		decision(t, KindReleased, Released{Intent: 3}),
		decision(t, KindSubmitted, Submitted{Intent: 4, Kind: "delegate"}),
		decision(t, KindConfirmed, Confirmed{Intent: 4}),
		decision(t, KindGuarded, Guarded{Kind: "go_dark"}),
		decision(t, KindIncident, Incident{Kind: "stall"}),
	}
	var a Audit
	for _, r := range recs {
		if err := a.Add(r); err != nil {
			t.Fatal(err)
		}
	}
	want := Tally{Payments: 3, Refills: 1, Confirmed: 3, Paid: 2, Released: 1,
		Settled: map[string]int{"send": 1, "delegate": 1}, Guarded: 1, Incidents: 1}
	if !reflect.DeepEqual(a.Tally, want) {
		t.Fatalf("audit %+v, want %+v", a.Tally, want)
	}
	for _, r := range []eventlog.Record{
		decision(t, KindConfirmed, Confirmed{Intent: 1}), // confirmed twice
		decision(t, KindConfirmed, Confirmed{Intent: 3}), // released, then confirmed
	} {
		if err := a.Add(r); err != nil {
			t.Fatal(err)
		}
	}
	if a.DoublePaid != 2 {
		t.Fatalf("double paid %d, want 2", a.DoublePaid)
	}
}
