package ui

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"go.uber.org/goleak"
)

func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

// started is a running continuum-ui command: its base URL and a stop
// function that waits for it to exit.
type started struct {
	// base is the http:// URL of the command's listener.
	base string
	// client sends every request; stop closes its idle connections so the
	// server's shutdown does not wait on them.
	client *http.Client
}

// start runs cmd with --addr on loopback port 0 and reads the listen line.
func start(t *testing.T, cmd func(context.Context, []string, io.Writer) error, args ...string) (started, func()) {
	t.Helper()
	pr, pw := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		err := cmd(ctx, append([]string{"--addr", "127.0.0.1:0"}, args...), pw)
		_ = pw.Close()
		done <- err
	}()

	line, err := bufio.NewReader(pr).ReadString('\n')
	if err != nil {
		cancel()
		t.Fatalf("no listen line: %v (%v)", err, <-done)
	}
	go func() { _, _ = io.Copy(io.Discard, pr) }()

	tr := &http.Transport{}
	s := started{
		base:   "http://" + strings.TrimSpace(strings.TrimPrefix(line, "listening on ")),
		client: &http.Client{Transport: tr},
	}
	return s, func() {
		tr.CloseIdleConnections()
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("command: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("command did not stop")
		}
	}
}

func (s started) fetch(t *testing.T, path string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, s.base+path, nil)
	if err != nil {
		t.Fatal(err)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, string(b)
}

// requireSPA checks s serves the embedded build: index.html, its hashed
// assets, the index.html fallback for client routes and a 404 for a
// missing asset.
func requireSPA(t *testing.T, s started) {
	t.Helper()
	resp, index := s.fetch(t, "/")
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") {
		t.Fatalf("index: %d %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}

	for ext, ctype := range map[string]string{"js": "text/javascript", "css": "text/css"} {
		m := regexp.MustCompile(`/assets/[A-Za-z0-9._-]+\.` + ext).FindString(index)
		if m == "" {
			t.Fatalf("index.html references no .%s asset", ext)
		}
		if r, _ := s.fetch(t, m); r.StatusCode != 200 || !strings.HasPrefix(r.Header.Get("Content-Type"), ctype) {
			t.Errorf("%s: %d %q, want %s", m, r.StatusCode, r.Header.Get("Content-Type"), ctype)
		}
	}

	if r, body := s.fetch(t, "/actors/3"); r.StatusCode != 200 || body != index {
		t.Errorf("client route did not fall back to index.html: %d", r.StatusCode)
	}

	if r, _ := s.fetch(t, "/assets/missing.js"); r.StatusCode != http.StatusNotFound {
		t.Errorf("missing asset: %d", r.StatusCode)
	}
}

func TestProxyCommand(t *testing.T) {
	t.Parallel()
	e := newEngine(t)
	s, stop := start(t, Proxy, "--api", e.srv.URL)
	defer stop()

	requireSPA(t, s)
	if r, body := s.fetch(t, "/api/overview"); r.StatusCode != 200 || body != "engine GET /api/overview " {
		t.Errorf("api: %d %q", r.StatusCode, body)
	}
}

func TestProxyUsageErrors(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{
		{},
		{"--api", "engine:8088"},
		{"--api", "http://127.0.0.1:1", "--addr", "0.0.0.0:0"},
		{"--api", "http://127.0.0.1:1", "extra"},
	} {
		if err := Proxy(t.Context(), args, io.Discard); err == nil {
			t.Errorf("Proxy(%q) succeeded", args)
		}
	}
}
