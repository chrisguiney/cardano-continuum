package ui

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// engine is a fake engine API: it echoes each request, streams two SSE
// frames on /api/decisions with the second held until release closes, and
// counts the requests that reach it.
type engine struct {
	// srv serves the fake API on a loopback port 0 listener.
	srv *httptest.Server
	// release lets /api/decisions write its second frame.
	release chan struct{}
	// hits counts requests that reached the engine.
	hits atomic.Int64
}

func newEngine(t *testing.T) *engine {
	t.Helper()
	e := &engine{
		release: make(chan struct{}),
	}
	e.srv = httptest.NewServer(http.HandlerFunc(e.serve))
	t.Cleanup(e.srv.Close)
	return e
}

func (e *engine) serve(w http.ResponseWriter, r *http.Request) {
	e.hits.Add(1)
	if r.URL.Path != "/api/decisions" {
		b, _ := io.ReadAll(r.Body)
		_, _ = io.WriteString(w, "engine "+r.Method+" "+r.URL.RequestURI()+" "+string(b))
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	_, _ = io.WriteString(w, "id: 1\ndata: {}\n\n")
	if err := http.NewResponseController(w).Flush(); err != nil {
		return
	}

	select {
	case <-e.release:
	case <-r.Context().Done():
		return
	}
	_, _ = io.WriteString(w, "id: 2\ndata: {}\n\n")
}

// front starts the proxy over e and a fake UI that names the path it got.
func front(t *testing.T, e *engine) *httptest.Server {
	t.Helper()
	api, err := url.Parse(e.srv.URL)
	if err != nil {
		t.Fatal(err)
	}

	ui := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "ui "+r.URL.Path) })
	srv := httptest.NewServer(newHandler(api, ui, newProber(api, 0, time.Now), slog.New(slog.DiscardHandler)))
	t.Cleanup(srv.Close)
	return srv
}

func do(t *testing.T, method, u, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, u, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(b)
}

func TestProxyRoutes(t *testing.T) {
	t.Parallel()
	e := newEngine(t)
	srv := front(t, e)
	tests := []struct {
		name, method, path, body string
		code                     int
		want                     string
	}{
		{"api get with query", "GET", "/api/actors?limit=5&sort=txs", "", 200, "engine GET /api/actors?limit=5&sort=txs"},
		{"api post with body", "POST", "/api/control", `{"op":"pause"}`, 200, `engine POST /api/control {"op":"pause"}`},
		{"spa root", "GET", "/", "", 200, "ui /"},
		{"client route", "GET", "/actors/3", "", 200, "ui /actors/3"},
		{"api lookalike is the ui", "GET", "/apix/overview", "", 200, "ui /apix/overview"},
		{"the bare api prefix is not forwarded", "GET", "/api", "", 200, "ui /api"},
		{"healthz is the proxy's own", "GET", "/healthz", "", 200, `{"status":"ok"}`},
		{"post outside the api", "POST", "/actors", "x", http.StatusMethodNotAllowed, ""},
	}
	for _, tt := range tests {
		code, body := do(t, tt.method, srv.URL+tt.path, tt.body)
		if code != tt.code || (tt.want != "" && strings.TrimSpace(body) != tt.want) {
			t.Errorf("%s: %s %s = %d %q, want %d %q", tt.name, tt.method, tt.path, code, body, tt.code, tt.want)
		}
	}
}

// TestProxyForwardsOnlyTheAPI: the target is fixed, and a path that
// cleans to somewhere outside /api never reaches it.
func TestProxyForwardsOnlyTheAPI(t *testing.T) {
	t.Parallel()
	e := newEngine(t)
	srv := front(t, e)

	for _, p := range []string{"/api/../healthz", "/api/%2e%2e/healthz", "/api//overview"} {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL, nil)
		if err != nil {
			t.Fatal(err)
		}

		req.URL.Opaque = p
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", p, resp.StatusCode)
		}
	}

	if n := e.hits.Load(); n != 0 {
		t.Errorf("%d requests reached the engine", n)
	}
}

func TestProxyBoundsTheBody(t *testing.T) {
	t.Parallel()
	e := newEngine(t)
	srv := front(t, e)

	code, _ := do(t, http.MethodPost, srv.URL+"/api/control", strings.Repeat("x", maxBody+1))
	if code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized body = %d, want 413", code)
	}

	if n := e.hits.Load(); n != 0 {
		t.Errorf("an oversized body reached the engine (%d requests)", n)
	}
}

func TestProxyEngineDown(t *testing.T) {
	t.Parallel()
	e := newEngine(t)
	srv := front(t, e)
	e.srv.Close()

	if code, body := do(t, http.MethodGet, srv.URL+"/api/overview", ""); code != http.StatusBadGateway || !strings.Contains(body, "engine unavailable") {
		t.Errorf("engine down = %d %q, want 502", code, body)
	}
}

// TestProxyStreamsEvents: each SSE frame reaches the browser as the engine
// flushes it. The engine holds its second frame until the first has
// arrived through the proxy, so a buffering proxy deadlocks here.
func TestProxyStreamsEvents(t *testing.T) {
	t.Parallel()
	e := newEngine(t)
	srv := front(t, e)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/decisions", nil)
	if err != nil {
		t.Fatal(err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q", ct)
	}

	r := bufio.NewReader(resp.Body)
	if line, err := r.ReadString('\n'); err != nil || line != "id: 1\n" {
		t.Fatalf("first frame: %q, %v", line, err)
	}

	close(e.release)
	for range 2 {
		if _, err := r.ReadString('\n'); err != nil {
			t.Fatal(err)
		}
	}

	if line, err := r.ReadString('\n'); err != nil || line != "id: 2\n" {
		t.Fatalf("second frame: %q, %v", line, err)
	}
}

func TestParseAPI(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		in string
		ok bool
	}{
		{"http://continuum-api.continuum.svc.cluster.local:8088", true},
		{"https://engine.example:443/", true},
		{"", false},
		{"continuum-api:8088", false},
		{"ftp://engine:21", false},
		{"http://", false},
		{"http://engine:8088/prefix", false},
		{"http://user:pw@engine:8088", false},
		{"http://engine:8088?x=1", false},
	} {
		if _, err := parseAPI(tt.in); (err == nil) != tt.ok {
			t.Errorf("parseAPI(%q) err = %v, want ok %v", tt.in, err, tt.ok)
		}
	}
}
