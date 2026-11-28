package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/chrisguiney/cardano-continuum/internal/engine"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/httpapi/view"
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
		{"clear weights", `{"request_id":"r1","op":"clear_weights"}`, true},
		{"clear weights with weights", `{"request_id":"r1","op":"clear_weights","weights":{"send":1}}`, false},
		{"clear weights with empty weights", `{"request_id":"r1","op":"clear_weights","weights":{}}`, false},
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
		{"scenario with an actor", `{"request_id":"r1","op":"inject","scenario":"go_dark:3"}`, true},
		{"upgrade with epochs and image", `{"request_id":"r1","op":"inject","scenario":"upgrade:2:ghcr.io/blinklabs-io/dingo:main"}`, true},
		{"image by digest", `{"request_id":"r1","op":"inject","scenario":"upgrade:ghcr.io/x/y@sha256:0123abcd"}`, true},
		{"empty argument", `{"request_id":"r1","op":"inject","scenario":"go_dark:"}`, false},
		{"space in argument", `{"request_id":"r1","op":"inject","scenario":"upgrade:a b"}`, false},
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

// TestLoggedControlIsEngineControl: a submitted control lands in the log
// as the engine's Control body, every field carried.
func TestLoggedControlIsEngineControl(t *testing.T) {
	log := &eventlog.MemLog{}
	lc, err := NewLogControls(context.Background(), log, func() sched.Time { return 5 })
	if err != nil {
		t.Fatal(err)
	}
	c := Control{RequestID: "r1", Op: OpSetWeights, K: 3, Weights: map[string]float64{"send": 2}, Scenario: "s"}
	if _, err := lc.Submit(context.Background(), c); err != nil {
		t.Fatal(err)
	}

	var body []byte
	if err := log.Scan(context.Background(), 1, func(r eventlog.Record) error { body = r.Body; return nil }); err != nil {
		t.Fatal(err)
	}
	if want := `{"request_id":"r1","op":"set_weights","k":3,"weights":{"send":2},"scenario":"s"}`; string(body) != want {
		t.Fatalf("body %s, want %s", body, want)
	}
}

// TestControlsReadBackWhatTheEngineApplied: a control posted to the API
// shows in GET /api/controls once the engine's answer is in the log, under
// the seq its receipt gave.
func TestControlsReadBackWhatTheEngineApplied(t *testing.T) {
	ctx := context.Background()
	log := &eventlog.MemLog{}
	lc := newLogControls(t, log)
	proj := view.NewProjection()
	src := view.NewLogSource(view.LogSourceConfig{Projection: proj})
	srv := httptest.NewServer(NewHandler(Config{Source: src, Controls: lc, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}))
	t.Cleanup(srv.Close)

	code, body := post(t, srv.URL+"/api/control", `{"request_id":"ui-1","op":"set_weights","weights":{"send":2}}`)
	if code != 202 {
		t.Fatalf("post: %d %s", code, body)
	}
	var rc Receipt
	if err := json.Unmarshal(body, &rc); err != nil {
		t.Fatal(err)
	}

	ans, err := eventlog.Encode(engine.Controlled{RequestID: "ui-1", Op: "set_weights"})
	if err != nil {
		t.Fatal(err)
	}
	if err := log.Append(ctx, eventlog.Record{At: 7, Class: eventlog.Decision, Kind: engine.KindControlled, Body: ans}); err != nil {
		t.Fatal(err)
	}
	if err := log.Scan(ctx, 1, func(r eventlog.Record) error { proj.Apply(r); return nil }); err != nil {
		t.Fatal(err)
	}

	code, body = get(t, srv.URL+"/api/controls")
	if code != 200 {
		t.Fatalf("get: %d %s", code, body)
	}
	var got ControlState
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got.K != 1 || got.Paused || got.Weights["send"] != 2 || len(got.Weights) != 1 ||
		got.Last == nil || got.Last.RequestID != "ui-1" || got.Last.Seq != rc.Seq || got.Last.At != 7 {
		t.Fatalf("state %s, receipt seq %d", body, rc.Seq)
	}
}
