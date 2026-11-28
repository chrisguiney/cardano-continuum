package ui

import (
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"
)

// flaky is a fake engine whose /api/capabilities answers code, or nothing
// once closed.
type flaky struct {
	// srv serves the fake engine on a loopback port 0 listener.
	srv *httptest.Server
	// code is the status /api/capabilities answers.
	code atomic.Int64
}

func newFlaky(t *testing.T, code int) *flaky {
	t.Helper()
	f := &flaky{}
	f.code.Store(int64(code))
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(int(f.code.Load()))
		_, _ = io.WriteString(w, `{}`)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *flaky) url(t *testing.T) *url.URL {
	t.Helper()
	u, err := url.Parse(f.srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

// closedURL is a loopback address nothing listens on.
func closedURL(t *testing.T) *url.URL {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return &url.URL{Scheme: "http", Host: addr}
}

func TestStatusStartingThenUp(t *testing.T) {
	t.Parallel()
	f := newFlaky(t, http.StatusServiceUnavailable)
	now := time.Unix(1000, 0)
	p := newProber(f.url(t), 0, func() time.Time { return now })

	st := p.Status(t.Context())
	if st.Engine != "starting" || st.LastError == "" || !st.Since.Equal(now) {
		t.Fatalf("503 before any success: %+v", st)
	}

	now = now.Add(time.Minute)
	if st := p.Status(t.Context()); st.Engine != "starting" || !st.Since.Equal(time.Unix(1000, 0)) {
		t.Fatalf("since moved while the state did not: %+v", st)
	}

	f.code.Store(http.StatusOK)
	if st := p.Status(t.Context()); st.Engine != "up" || st.LastError != "" || !st.Since.Equal(now) {
		t.Fatalf("after the engine answered: %+v", st)
	}
}

func TestStatusUnreachableAfterUp(t *testing.T) {
	t.Parallel()
	f := newFlaky(t, http.StatusOK)
	p := newProber(f.url(t), 0, time.Now)
	if st := p.Status(t.Context()); st.Engine != "up" {
		t.Fatalf("%+v", st)
	}

	f.srv.Close()
	if st := p.Status(t.Context()); st.Engine != "unreachable" || st.LastError == "" {
		t.Fatalf("engine gone: %+v", st)
	}
}

func TestStatusNeverUpIsStarting(t *testing.T) {
	t.Parallel()
	p := newProber(closedURL(t), 0, time.Now)
	if st := p.Status(t.Context()); st.Engine != "starting" || st.LastError == "" {
		t.Fatalf("%+v", st)
	}
}

func TestStatusIsCached(t *testing.T) {
	t.Parallel()
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	now := time.Unix(1000, 0)
	p := newProber(u, 3*time.Second, func() time.Time { return now })

	for range 5 {
		p.Status(t.Context())
	}
	if hits.Load() != 1 {
		t.Fatalf("%d probes inside the ttl, want 1", hits.Load())
	}

	now = now.Add(4 * time.Second)
	p.Status(t.Context())
	if hits.Load() != 2 {
		t.Fatalf("%d probes after the ttl, want 2", hits.Load())
	}
}

func TestStatusEndpointAnswersWithoutTheEngine(t *testing.T) {
	t.Parallel()
	api := closedURL(t)
	ui := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	srv := httptest.NewServer(newHandler(api, ui, newProber(api, 0, time.Now), slog.New(slog.DiscardHandler)))
	t.Cleanup(srv.Close)

	code, body := do(t, http.MethodGet, srv.URL+"/api/_ui/status", "")
	if code != http.StatusOK {
		t.Fatalf("status %d: %s", code, body)
	}

	var st Status
	if err := json.Unmarshal([]byte(body), &st); err != nil || st.Engine != "starting" || st.LastError == "" {
		t.Fatalf("%q: %+v, %v", body, st, err)
	}

	if st.Now.IsZero() {
		t.Errorf("status carries no now: %q", body)
	}
}
