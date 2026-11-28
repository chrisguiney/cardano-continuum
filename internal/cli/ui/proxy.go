package ui

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path"
	"strings"
	"time"

	"github.com/chrisguiney/cardano-continuum/internal/cli/internal/serving"
	"github.com/chrisguiney/cardano-continuum/internal/httpapi"
	"github.com/chrisguiney/cardano-continuum/web"
)

const proxyUsage = "usage: continuum-ui proxy --api http://HOST:PORT [--addr HOST:PORT] [--allow-remote]"

// maxBody bounds a forwarded request body, at the engine's own bound, so an
// oversized body is refused here before it ties up a connection to the
// engine.
const maxBody = httpapi.MaxBodyBytes

// Proxy serves the web UI and forwards /api/* to the engine API that --api
// names.
func Proxy(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("proxy", flag.ContinueOnError)
	apiURL := fs.String("api", os.Getenv("CONTINUUM_API"), "engine API base URL, http://HOST:PORT (default $CONTINUUM_API)")
	addr := fs.String("addr", "127.0.0.1:8088", "listen address; loopback only unless --allow-remote")
	remote := fs.Bool("allow-remote", false, "accept a non-loopback --addr; the UI and the API it forwards have no authentication")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if fs.NArg() != 0 {
		return errors.New(proxyUsage)
	}

	api, err := parseAPI(*apiURL)
	if err != nil {
		return fmt.Errorf("--api: %w (%s)", err, proxyUsage)
	}

	if err := serving.CheckAddr(*addr, *remote); err != nil {
		return err
	}

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		return err
	}

	if _, err := fmt.Fprintf(out, "listening on %s\n", ln.Addr()); err != nil {
		_ = ln.Close()
		return err
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, nil)).With("component", "ui")
	log.Info("forwarding the api", "api", api.Redacted())
	return httpapi.Serve(ctx, ln, newHandler(api, web.Handler(), newProber(api, probeTTL, time.Now), log), log)
}

// parseAPI accepts an http or https base URL with a host and nothing else:
// no path, query or credentials, so every forwarded path is the browser's.
func parseAPI(s string) (*url.URL, error) {
	u, err := url.Parse(s)
	if err != nil {
		return nil, err
	}

	switch {
	case u.Scheme != "http" && u.Scheme != "https":
		return nil, fmt.Errorf("%q is not an http or https URL", s)
	case u.Host == "":
		return nil, fmt.Errorf("%q names no host", s)
	case u.User != nil, u.RawQuery != "", u.Fragment != "", u.Path != "" && u.Path != "/":
		return nil, fmt.Errorf("%q has more than a scheme, host and port", s)
	}
	return &url.URL{Scheme: u.Scheme, Host: u.Host}, nil
}

// newHandler routes /api/* to api and every other GET to ui. /healthz is
// the proxy's own, so the UI's readiness does not follow the engine's, and
// so is /api/_ui/status, which the UI polls to tell a starting engine from a
// broken one.
//
// Only a clean path under /api/ is forwarded: the target is fixed, and a
// path that cleans elsewhere ("/api/../x") would otherwise reach engine
// routes outside the API.
func newHandler(api *url.URL, ui http.Handler, st *prober, log *slog.Logger) http.Handler {
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(api)
			pr.SetXForwarded()
		},
		// Write each chunk through as the engine sends it: an SSE frame
		// must reach the browser when the engine flushes it, not when a
		// buffer fills.
		FlushInterval: -1,
		ErrorLog:      slog.NewLogLogger(log.Handler(), slog.LevelWarn),
		ErrorHandler:  proxyError(log),
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		switch {
		case p == "/healthz":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"status":"ok"}`+"\n")
		case p == statusPath:
			writeStatus(w, r, st)
		case strings.HasPrefix(p, "/api/"):
			forward(w, r, rp)
		case r.Method != http.MethodGet && r.Method != http.MethodHead:
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		default:
			ui.ServeHTTP(w, r)
		}
	})
}

// forward sends one API request to the engine, refusing an unclean path
// and a body over maxBody.
func forward(w http.ResponseWriter, r *http.Request, rp *httputil.ReverseProxy) {
	if path.Clean(r.URL.Path) != r.URL.Path {
		http.NotFound(w, r)
		return
	}

	if r.ContentLength > maxBody {
		http.Error(w, `{"error":"request body too large"}`, http.StatusRequestEntityTooLarge)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	rp.ServeHTTP(w, r)
}

// proxyError answers a request the engine did not: 413 for a body cut off
// at maxBody, 502 otherwise. A browser that left is not worth a warning.
func proxyError(log *slog.Logger) func(http.ResponseWriter, *http.Request, error) {
	return func(w http.ResponseWriter, r *http.Request, err error) {
		var mbe *http.MaxBytesError
		switch {
		case errors.As(err, &mbe):
			http.Error(w, `{"error":"request body too large"}`, http.StatusRequestEntityTooLarge)
			return
		case errors.Is(err, context.Canceled):
			log.Debug("client left", "path", r.URL.Path)
		default:
			log.Warn("engine api request failed", "path", r.URL.Path, "err", err)
		}
		http.Error(w, `{"error":"engine unavailable"}`, http.StatusBadGateway)
	}
}
