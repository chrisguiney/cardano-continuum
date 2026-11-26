package httpapi

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

func TestParseControl(t *testing.T) {
	tests := []struct {
		name string
		body string
		ok   bool
	}{
		{"pause", `{"request_id":"r1","op":"pause"}`, true},
		{"resume", `{"request_id":"r1","op":"resume"}`, true},
		{"set k", `{"request_id":"r1","op":"set_k","k":4}`, true},
		{"set weights", `{"request_id":"r1","op":"set_weights","weights":{"send":2.5,"idle":0}}`, true},
		{"inject", `{"request_id":"r1","op":"inject","scenario":"fork-storm"}`, true},
		{"unknown field", `{"request_id":"r1","op":"pause","extra":1}`, false},
		{"unknown op", `{"request_id":"r1","op":"reboot"}`, false},
		{"no request id", `{"op":"pause"}`, false},
		{"bad request id", `{"request_id":"a b","op":"pause"}`, false},
		{"long request id", `{"request_id":"` + strings.Repeat("a", 65) + `","op":"pause"}`, false},
		{"k zero", `{"request_id":"r1","op":"set_k","k":0}`, false},
		{"k too big", `{"request_id":"r1","op":"set_k","k":1001}`, false},
		{"k on pause", `{"request_id":"r1","op":"pause","k":2}`, false},
		{"no weights", `{"request_id":"r1","op":"set_weights"}`, false},
		{"negative weight", `{"request_id":"r1","op":"set_weights","weights":{"send":-1}}`, false},
		{"huge weight", `{"request_id":"r1","op":"set_weights","weights":{"send":1e9}}`, false},
		{"bad weight name", `{"request_id":"r1","op":"set_weights","weights":{"Send!":1}}`, false},
		{"no scenario", `{"request_id":"r1","op":"inject"}`, false},
		{"bad scenario", `{"request_id":"r1","op":"inject","scenario":"../x"}`, false},
		{"trailing data", `{"request_id":"r1","op":"pause"} {}`, false},
		{"not json", `pause`, false},
	}
	for _, tt := range tests {
		_, err := ParseControl(strings.NewReader(tt.body))
		if (err == nil) != tt.ok {
			t.Errorf("%s: err %v, want ok=%t", tt.name, err, tt.ok)
		}
		if err != nil && !errors.Is(err, ErrBadRequest) {
			t.Errorf("%s: %v does not wrap ErrBadRequest", tt.name, err)
		}
	}
}

func newLogControls(t *testing.T, l *eventlog.MemLog) *LogControls {
	t.Helper()
	c, err := NewLogControls(context.Background(), l, func() sched.Time { return 42 })
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func scanControls(t *testing.T, l *eventlog.MemLog) []eventlog.Record {
	t.Helper()
	var out []eventlog.Record
	if err := l.Scan(context.Background(), 1, func(r eventlog.Record) error {
		out = append(out, r)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestLogControlsAppendsExactlyOneControlRecord(t *testing.T) {
	l := &eventlog.MemLog{}
	c := newLogControls(t, l)
	rc, err := c.Submit(context.Background(), Control{RequestID: "r1", Op: OpSetK, K: 3})
	if err != nil {
		t.Fatal(err)
	}
	recs := scanControls(t, l)
	if len(recs) != 1 {
		t.Fatalf("%d records", len(recs))
	}
	r := recs[0]
	if r.Kind != eventlog.KindControl || r.Class != eventlog.Input || r.At != 42 || r.Seq != rc.Seq || rc.Duplicate {
		t.Fatalf("record %+v receipt %+v", r, rc)
	}
	var got Control
	if err := eventlog.Decode(r.Body, &got); err != nil {
		t.Fatal(err)
	}
	if got.RequestID != "r1" || got.Op != OpSetK || got.K != 3 {
		t.Fatalf("body %+v", got)
	}
}

func TestLogControlsIdempotentPerRequestID(t *testing.T) {
	l := &eventlog.MemLog{}
	c := newLogControls(t, l)
	first, err := c.Submit(context.Background(), Control{RequestID: "r1", Op: OpPause})
	if err != nil {
		t.Fatal(err)
	}
	again, err := c.Submit(context.Background(), Control{RequestID: "r1", Op: OpPause})
	if err != nil {
		t.Fatal(err)
	}
	if !again.Duplicate || again.Seq != first.Seq {
		t.Fatalf("receipt %+v, first %+v", again, first)
	}
	if _, err := c.Submit(context.Background(), Control{RequestID: "r2", Op: OpResume}); err != nil {
		t.Fatal(err)
	}
	if n := len(scanControls(t, l)); n != 2 {
		t.Fatalf("%d records", n)
	}
}

func TestLogControlsRememberIDsAcrossRestart(t *testing.T) {
	l := &eventlog.MemLog{}
	if _, err := newLogControls(t, l).Submit(context.Background(), Control{RequestID: "r1", Op: OpPause}); err != nil {
		t.Fatal(err)
	}
	rc, err := newLogControls(t, l).Submit(context.Background(), Control{RequestID: "r1", Op: OpPause})
	if err != nil || !rc.Duplicate {
		t.Fatalf("receipt %+v err %v", rc, err)
	}
	if n := len(scanControls(t, l)); n != 1 {
		t.Fatalf("%d records", n)
	}
}
