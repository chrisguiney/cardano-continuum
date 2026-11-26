package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
)

// startServe runs `continuum serve` on port 0 and returns its base URL and
// a stop function that waits for it to exit.
func startServe(t *testing.T, args ...string) (string, func()) {
	t.Helper()
	pr, pw := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		err := run(ctx, append([]string{"serve", "--addr", "127.0.0.1:0"}, args...), pw)
		_ = pw.Close()
		done <- err
	}()
	line, err := bufio.NewReader(pr).ReadString('\n')
	if err != nil {
		cancel()
		t.Fatalf("no listen line: %v (%v)", err, <-done)
	}
	go func() { _, _ = io.Copy(io.Discard, pr) }()
	addr := strings.TrimSpace(strings.TrimPrefix(line, "listening on "))
	return "http://" + addr, func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("serve: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("serve did not stop")
		}
	}
}

func fakeRunLog(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	var out strings.Builder
	args := []string{"run", "--fake", "--epochs", "1", "--out", dir, "--wallets", "20", "--epoch-slots", "60"}
	if err := run(context.Background(), args, &out); err != nil {
		t.Fatalf("run: %v\n%s", err, out.String())
	}
	return filepath.Join(dir, "events.log")
}

func getJSON(t *testing.T, url string, v any) int {
	t.Helper()
	resp, err := http.Get(url) //nolint:gosec,noctx // loopback test server
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode
}

func TestServeOverFinishedRun(t *testing.T) {
	log := fakeRunLog(t)
	base, stop := startServe(t, "--log", log, "--epoch-slots", "60")
	defer stop()

	var snap struct {
		LastSeq    uint64 `json:"last_seq"`
		Population struct{ Total int }
	}
	if code := getJSON(t, base+"/api/overview", &snap); code != 200 || snap.LastSeq == 0 || snap.Population.Total != 20 {
		t.Fatalf("overview %d %+v", code, snap)
	}
	var page struct{ Total int }
	if code := getJSON(t, base+"/api/actors?limit=5&sort=txs&dir=desc", &page); code != 200 || page.Total != 20 {
		t.Fatalf("actors %d %+v", code, page)
	}
	var e struct{ Error string }
	if code := getJSON(t, base+"/api/actors?sort=bogus", &e); code != 400 {
		t.Fatalf("bad sort %d", code)
	}
	resp, err := http.Post(base+"/api/control", "application/json", strings.NewReader(`{"request_id":"r1","op":"pause"}`)) //nolint:gosec,noctx // loopback test server
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotImplemented {
		t.Fatalf("control without --controls: %d", resp.StatusCode)
	}
}

func TestServeControlsAppendToTheLog(t *testing.T) {
	log := fakeRunLog(t)
	before := controlCount(t, log, eventlog.KindControl)
	base, stop := startServe(t, "--log", log, "--controls", "--epoch-slots", "60")
	body := `{"request_id":"r1","op":"set_k","k":2}`
	for i, want := range []int{http.StatusAccepted, http.StatusOK} {
		resp, err := http.Post(base+"/api/control", "application/json", strings.NewReader(body)) //nolint:gosec,noctx // loopback test server
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != want {
			t.Fatalf("post %d: %d", i, resp.StatusCode)
		}
	}
	stop()
	if got := controlCount(t, log, eventlog.KindControl); got != before+1 {
		t.Fatalf("%d control records, want %d", got, before+1)
	}
}

func controlCount(t *testing.T, path, kind string) int {
	t.Helper()
	l, err := eventlog.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	n := 0
	if err := l.Scan(context.Background(), 1, func(r eventlog.Record) error {
		if r.Kind == kind {
			n++
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestServeUsageErrors(t *testing.T) {
	ctx := context.Background()
	var out strings.Builder
	for _, args := range [][]string{
		{"serve"},
		{"serve", "--log", "/nonexistent/events.log"},
		{"serve", "--log", fakeRunLog(t), "--store", "/nonexistent/s.db"},
		{"serve", "--log", fakeRunLog(t), "--addr", "0.0.0.0:0"},
	} {
		if err := run(ctx, args, &out); err == nil {
			t.Errorf("%v succeeded", args)
		}
	}
}
