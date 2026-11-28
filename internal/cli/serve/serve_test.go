package serve

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

// served is a running `continuum serve`: its base URL and the client every
// request to it goes through.
type served struct {
	base string
	c    *http.Client
}

// startServe runs `continuum serve` on port 0 and returns it with a stop
// function that waits for it to exit. Stop closes the client's connections
// first: a kept-alive connection, or a spare one the transport dialled for a
// request another connection then served, is one the server's Shutdown
// waits on until its grace runs out.
func startServe(t *testing.T, args ...string) (served, func()) {
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
	tr := &http.Transport{}
	srv := served{base: "http://" + addr, c: &http.Client{Transport: tr}}
	return srv, func() {
		tr.CloseIdleConnections()
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

func (s served) post(t *testing.T, path, body string) int {
	t.Helper()
	resp, err := s.c.Post(s.base+path, "application/json", strings.NewReader(body)) //nolint:noctx // loopback test server
	if err != nil {
		t.Fatal(err)
	}
	// Reading to EOF lets the transport keep the connection idle, where
	// CloseIdleConnections reaches it.
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	return resp.StatusCode
}

func (s served) getJSON(t *testing.T, path string, v any) int {
	t.Helper()
	resp, err := s.c.Get(s.base + path) //nolint:noctx // loopback test server
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
	t.Parallel()
	log := fakeRunLog(t)
	srv, stop := startServe(t, "--log", log, "--epoch-slots", "60")
	defer stop()

	var snap struct {
		LastSeq    uint64 `json:"last_seq"`
		Population struct{ Total int }
	}
	// The 20 wallets, and m1's three exchanges.
	const actors = 23
	if code := srv.getJSON(t, "/api/overview", &snap); code != 200 || snap.LastSeq == 0 || snap.Population.Total != actors {
		t.Fatalf("overview %d %+v", code, snap)
	}
	var page struct{ Total int }
	if code := srv.getJSON(t, "/api/actors?limit=5&sort=txs&dir=desc", &page); code != 200 || page.Total != actors {
		t.Fatalf("actors %d %+v", code, page)
	}
	var e struct{ Error string }
	if code := srv.getJSON(t, "/api/actors?sort=bogus", &e); code != 400 {
		t.Fatalf("bad sort %d", code)
	}
	if code := srv.post(t, "/api/control", `{"request_id":"r1","op":"pause"}`); code != http.StatusNotImplemented {
		t.Fatalf("control without --controls: %d", code)
	}
}

func TestServeControlsAppendToTheLog(t *testing.T) {
	t.Parallel()
	log := fakeRunLog(t)
	before := controlCount(t, log, eventlog.KindControl)
	srv, stop := startServe(t, "--log", log, "--controls", "--epoch-slots", "60")
	body := `{"request_id":"r1","op":"set_k","k":2}`
	for i, want := range []int{http.StatusAccepted, http.StatusOK} {
		if code := srv.post(t, "/api/control", body); code != want {
			t.Fatalf("post %d: %d", i, code)
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
	t.Parallel()
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

// TestServeIsAPIOnly: continuum serve links no web UI, so every path
// outside the API is a 404; continuum-ui serve adds the UI.
func TestServeIsAPIOnly(t *testing.T) {
	t.Parallel()
	log := fakeRunLog(t)
	srv, stop := startServe(t, "--log", log, "--epoch-slots", "60")
	defer stop()

	for _, p := range []string{"/", "/actors/3", "/index.html"} {
		resp, err := srv.c.Get(srv.base + p) //nolint:noctx // loopback test server
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", p, resp.StatusCode)
		}
	}

	var caps struct{ Controls bool }
	if code := srv.getJSON(t, "/api/capabilities", &caps); code != 200 || caps.Controls {
		t.Errorf("capabilities: %d %+v", code, caps)
	}
}
