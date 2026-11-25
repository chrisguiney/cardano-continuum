package engine

import (
	"context"
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
	recs := []eventlog.Record{
		decision(t, KindSubmitted, Submitted{Intent: 1}),
		decision(t, KindSubmitted, Submitted{Intent: 1}), // a rebuild is not a second payment
		decision(t, KindRefill, Refill{Intent: 2}),
		decision(t, KindConfirmed, Confirmed{Intent: 1}),
		decision(t, KindConfirmed, Confirmed{Intent: 2}),
		decision(t, KindSubmitted, Submitted{Intent: 3}),
		decision(t, KindReleased, Released{Intent: 3}),
	}
	var a Audit
	for _, r := range recs {
		if err := a.Add(r); err != nil {
			t.Fatal(err)
		}
	}
	want := Tally{Payments: 2, Refills: 1, Confirmed: 2, Paid: 1, Released: 1}
	if a.Tally != want {
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
