package httpapi

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/chrisguiney/cardano-continuum/internal/agenda"
	"github.com/chrisguiney/cardano-continuum/internal/engine"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
)

func enc(t testing.TB, v any) []byte {
	t.Helper()
	b, err := eventlog.Encode(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func txid(b byte) engine.TxID { return engine.TxID(bytes.Repeat([]byte{b}, 32)) }

func fixture(t testing.TB) []eventlog.Record {
	t.Helper()
	dec := agenda.Decision{
		Intent: agenda.Intent{Kind: "send", Actor: 1, Target: 2},
		Scores: []agenda.Score{{Kind: "idle", Value: 0.25, Why: "base"}, {Kind: "send", Value: 0.75, Target: 2, Why: "rich"}},
	}
	pt := eventlog.ChainPoint{Slot: 77, Hash: bytes.Repeat([]byte{7}, 32)}
	content := engine.BlockContent{Txs: []engine.TxDigest{{ID: txid(0xa2), Fee: 180000}}}
	return []eventlog.Record{
		{Class: eventlog.Header, Kind: eventlog.KindHeader, Body: enc(t, eventlog.HeaderBody{BinaryVersion: "m1"})},
		{At: 0, Class: eventlog.Decision, Kind: engine.KindSpawned, Actor: 1, Body: enc(t, engine.Spawned{Profile: "hodl"})},
		{At: 0, Class: eventlog.Decision, Kind: engine.KindSpawned, Actor: 2, Body: enc(t, engine.Spawned{Profile: "yield"})},
		{At: 5000, Class: eventlog.Decision, Kind: engine.KindTransition, Actor: 1, Body: enc(t, engine.Transition{From: "new", To: "active"})},
		{At: 6000, Class: eventlog.Decision, Kind: engine.KindIntentChosen, Actor: 1, Body: enc(t, dec)},
		{At: 6000, Class: eventlog.Decision, Kind: engine.KindSubmitted, Actor: 1, Body: enc(t, engine.Submitted{Intent: 9, ID: txid(0xa1), To: 2, Amount: 5_000_000, Fee: 170000})},
		{At: 7000, Class: eventlog.Input, Kind: eventlog.KindSubmitResult, Body: enc(t, engine.SubmitResult{Intent: 9, Verdict: engine.VerdictRejected, Reason: "bad"})},
		{At: 8000, Class: eventlog.Decision, Kind: engine.KindRefused, Actor: 1, Body: enc(t, engine.Refused{Intent: 9, ID: txid(0xa1), Why: "bad"})},
		{At: 9000, Class: eventlog.Decision, Kind: engine.KindSubmitted, Actor: 1, Body: enc(t, engine.Submitted{Intent: 9, ID: txid(0xa2), To: 2, Amount: 5_000_000, Fee: 180000})},
		{At: 10000, Class: eventlog.Input, Kind: eventlog.KindBlockApplied, Body: enc(t, eventlog.BlockApplied{Point: pt, Content: enc(t, content)})},
		{At: 20000, Class: eventlog.Decision, Kind: engine.KindConfirmed, Actor: 1, Body: enc(t, engine.Confirmed{Intent: 9, ID: txid(0xa2)})},
	}
}

func writeLog(t *testing.T, recs []eventlog.Record) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "events.log")
	l, err := eventlog.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Append(context.Background(), recs...); err != nil {
		t.Fatal(err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func newTail(t *testing.T, path string) (*Tail, *Projection, *Hub) {
	t.Helper()
	p, h := NewProjection(), NewHub(64, 4, 8)
	tl, err := NewTail(path, p, h)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tl.Close() })
	return tl, p, h
}

func TestTailBuildsProjection(t *testing.T) {
	path := writeLog(t, fixture(t))
	tl, p, h := newTail(t, path)
	n, err := tl.Poll(context.Background())
	if err != nil || n != 11 {
		t.Fatalf("polled %d: %v", n, err)
	}
	s := p.Snapshot(nil)
	if s.LastSeq != 11 || s.SimTimeMs != 20000 {
		t.Fatalf("snapshot %+v", s)
	}
	if s.Tip == nil || s.Tip.Slot != 77 || s.Blocks != 1 {
		t.Fatalf("tip %+v blocks %d", s.Tip, s.Blocks)
	}
	if s.Population.Total != 2 || p.ProfileCounts(nil)["hodl"] != 1 || s.Population.ByState["active"] != 1 {
		t.Fatalf("population %+v", s.Population)
	}
	if s.Ada.SubmittedLovelace != 10_000_000 || s.Ada.ConfirmedLovelace != 5_000_000 || s.Ada.FeesLovelace != 180000 {
		t.Fatalf("ada %+v", s.Ada)
	}
	if s.Txs.Submitted != 2 || s.Txs.Confirmed != 1 || s.Txs.Retries != 1 || s.Txs.Refused != 1 {
		t.Fatalf("txs %+v", s.Txs)
	}
	for _, inv := range s.Invariants {
		if !inv.OK {
			t.Errorf("invariant %+v", inv)
		}
	}

	tlm, err := p.Timeline(1)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, e := range tlm.Entries {
		kinds = append(kinds, e.Kind)
	}
	want := []string{"Spawned", "Transition", "IntentChosen", "Submitted", "SubmitResult", "Refused", "Submitted", "Confirmed"}
	if len(kinds) != len(want) {
		t.Fatalf("timeline %v", kinds)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("timeline %v", kinds)
		}
	}
	if tlm.Profile != "hodl" || tlm.State != "active" {
		t.Fatalf("traits %+v", tlm)
	}
	if _, err := p.Timeline(99); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown actor: %v", err)
	}

	// Only engine decisions reach the feed, with scores on the intent.
	sub, err := h.Subscribe(Filter{Kinds: []string{engine.KindIntentChosen}}, ptr(0))
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	if len(sub.Replay) != 1 || sub.Replay[0].Profile != "hodl" || len(sub.Replay[0].Scores) != 2 || sub.Replay[0].Intent != "send" {
		t.Fatalf("replay %+v", sub.Replay)
	}
	all, _ := h.Subscribe(Filter{}, ptr(0))
	defer all.Close()
	if len(all.Replay) != 8 {
		t.Fatalf("%d decisions in ring", len(all.Replay))
	}
}

func TestTailWaitsOutATornTail(t *testing.T) {
	recs := fixture(t)
	path := writeLog(t, recs[:3])
	tl, _, _ := newTail(t, path)
	if n, err := tl.Poll(context.Background()); err != nil || n != 3 {
		t.Fatalf("polled %d: %v", n, err)
	}
	frame, err := eventlog.AppendFrame(nil, withSeq(recs[3], 4))
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Write(frame[:len(frame)-3]); err != nil {
		t.Fatal(err)
	}
	if n, err := tl.Poll(context.Background()); err != nil || n != 0 {
		t.Fatalf("torn tail: polled %d: %v", n, err)
	}
	if _, err := f.Write(frame[len(frame)-3:]); err != nil {
		t.Fatal(err)
	}
	if n, err := tl.Poll(context.Background()); err != nil || n != 1 {
		t.Fatalf("completed tail: polled %d: %v", n, err)
	}
	// The reader never truncates what a live writer is still writing.
	if st, _ := os.Stat(path); st.Size() == 0 {
		t.Fatal("log emptied")
	}
}

func withSeq(r eventlog.Record, seq uint64) eventlog.Record { r.Seq = seq; return r }

func TestTailRefusesASeqGap(t *testing.T) {
	recs := fixture(t)
	path := writeLog(t, recs[:2])
	frame, err := eventlog.AppendFrame(nil, withSeq(recs[2], 9))
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(frame); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	tl, _, _ := newTail(t, path)
	if _, err := tl.Poll(context.Background()); !errors.Is(err, eventlog.ErrSeqGap) {
		t.Fatalf("got %v", err)
	}
}

func TestTailRunFollowsAppends(t *testing.T) {
	recs := fixture(t)
	path := writeLog(t, recs[:3])
	tl, _, h := newTail(t, path)
	if _, err := tl.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	sub, err := h.Subscribe(Filter{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- tl.Run(ctx) }()
	l, err := eventlog.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	if err := l.Append(ctx, recs[3]); err != nil {
		t.Fatal(err)
	}
	if d := recv(t, sub.C); d.Kind != engine.KindTransition {
		t.Fatalf("got %+v", d)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run returned %v", err)
	}
}
