package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	defaultHeartbeat    = 15 * time.Second
	defaultWriteTimeout = 10 * time.Second
)

// Config wires NewHandler. Controls may be nil, which turns POST
// /api/control into 501.
type Config struct {
	Source   Source
	Controls Controls
	// UI serves every GET path outside /api and /healthz, normally the
	// embedded single-page app; nil leaves those paths 404.
	UI     http.Handler
	Logger *slog.Logger
	// Heartbeat is the SSE comment interval; zero means 15s.
	Heartbeat time.Duration
	// WriteTimeout bounds each SSE write, so a client that stops reading
	// frees its goroutine; zero means 10s.
	WriteTimeout time.Duration
}

type api struct {
	Config
}

// NewHandler returns the API's routes:
//
//	GET  /healthz
//	GET  /api/capabilities
//	GET  /api/overview
//	GET  /api/actors?limit&offset&sort&dir&profile&state&role&delegated
//	GET  /api/actors/{id}
//	GET  /api/adversaries
//	GET  /api/actors/{id}/timeline
//	GET  /api/pools
//	GET  /api/findings?limit&offset&known=true|false
//	GET  /api/findings/{id}
//	GET  /api/decisions?actor&profile&kind&last_event_id   (SSE)
//	POST /api/control
func NewHandler(cfg Config) http.Handler {
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	cfg.Logger = cfg.Logger.With("component", "httpapi")
	if cfg.Heartbeat <= 0 {
		cfg.Heartbeat = defaultHeartbeat
	}
	if cfg.WriteTimeout <= 0 {
		cfg.WriteTimeout = defaultWriteTimeout
	}
	a := &api{cfg}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		a.json(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /api/capabilities", func(w http.ResponseWriter, _ *http.Request) {
		a.json(w, http.StatusOK, map[string]bool{"controls": a.Controls != nil})
	})
	mux.HandleFunc("GET /api/overview", a.overview)
	mux.HandleFunc("GET /api/actors", a.actors)
	mux.HandleFunc("GET /api/actors/{id}/timeline", a.timeline)
	mux.HandleFunc("GET /api/actors/{id}", a.actorDetail)
	mux.HandleFunc("GET /api/adversaries", a.adversaries)
	mux.HandleFunc("GET /api/pools", a.pools)
	mux.HandleFunc("GET /api/findings", a.findings)
	mux.HandleFunc("GET /api/findings/{id}", func(w http.ResponseWriter, _ *http.Request) {
		a.fail(w, fmt.Errorf("%w: finding", ErrNotFound))
	})
	mux.HandleFunc("GET /api/decisions", a.decisions)
	mux.HandleFunc("POST /api/control", a.control)
	if cfg.UI == nil {
		return mux
	}
	// Anything outside the API goes to the UI. Routing here rather than
	// registering "/" keeps an unknown /api path a 404 instead of HTML, and
	// leaves the mux's 405 for a wrong method on a real endpoint.
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/healthz" {
			mux.ServeHTTP(w, r)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		cfg.UI.ServeHTTP(w, r)
	})
}

func (a *api) json(w http.ResponseWriter, code int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		a.Logger.Error("encode response", "err", err)
		http.Error(w, `{"error":"internal error"}`, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_, _ = w.Write(append(b, '\n'))
}

// fail maps err to a status. Only the typed client errors reach the client
// verbatim; anything else is logged and answered generically.
func (a *api) fail(w http.ResponseWriter, err error) {
	var mbe *http.MaxBytesError
	switch {
	case errors.As(err, &mbe):
		a.json(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "request body too large"})
	case errors.Is(err, ErrBadRequest):
		a.json(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
	case errors.Is(err, ErrNotFound):
		a.json(w, http.StatusNotFound, map[string]string{"error": err.Error()})
	case errors.Is(err, ErrTooManyClients):
		a.json(w, http.StatusTooManyRequests, map[string]string{"error": err.Error()})
	default:
		a.Logger.Error("request failed", "err", err)
		a.json(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
	}
}

func (a *api) overview(w http.ResponseWriter, r *http.Request) {
	s, err := a.Source.Snapshot(r.Context())
	if err != nil {
		a.fail(w, err)
		return
	}
	a.json(w, http.StatusOK, s)
}

func (a *api) actors(w http.ResponseWriter, r *http.Request) {
	q, err := parseActorQuery(r)
	if err != nil {
		a.fail(w, err)
		return
	}
	pg, err := a.Source.Actors(r.Context(), q)
	if err != nil {
		a.fail(w, err)
		return
	}
	a.json(w, http.StatusOK, pg)
}

func (a *api) timeline(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseUint(r.PathValue("id"), 10, 64)
	if err != nil {
		a.fail(w, badf("actor id must be an unsigned integer"))
		return
	}
	t, err := a.Source.ActorTimeline(r.Context(), id)
	if err != nil {
		a.fail(w, err)
		return
	}
	a.json(w, http.StatusOK, t)
}

func (a *api) actorDetail(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseUint(r.PathValue("id"), 10, 64)
	if err != nil {
		a.fail(w, badf("actor id must be an unsigned integer"))
		return
	}
	d, err := a.Source.ActorDetail(r.Context(), id)
	if err != nil {
		a.fail(w, err)
		return
	}
	a.json(w, http.StatusOK, d)
}

func (a *api) adversaries(w http.ResponseWriter, r *http.Request) {
	v, err := a.Source.Adversaries(r.Context())
	if err != nil {
		a.fail(w, err)
		return
	}
	a.json(w, http.StatusOK, v)
}

func (a *api) pools(w http.ResponseWriter, r *http.Request) {
	v, err := a.Source.Pools(r.Context())
	if err != nil {
		a.fail(w, err)
		return
	}
	a.json(w, http.StatusOK, v)
}

func (a *api) findings(w http.ResponseWriter, r *http.Request) {
	q, err := parseActorQuery(r)
	if err != nil {
		a.fail(w, err)
		return
	}
	known, err := parseKnown(r)
	if err != nil {
		a.fail(w, err)
		return
	}
	f, err := a.Source.Findings(r.Context(), Page{Limit: q.Limit, Offset: q.Offset, Known: known})
	if err != nil {
		a.fail(w, err)
		return
	}
	a.json(w, http.StatusOK, f)
}

func (a *api) control(w http.ResponseWriter, r *http.Request) {
	if a.Controls == nil {
		a.json(w, http.StatusNotImplemented, map[string]string{"error": "controls are not enabled"})
		return
	}
	c, err := ParseControl(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err != nil {
		a.fail(w, err)
		return
	}
	rc, err := a.Controls.Submit(r.Context(), c)
	if err != nil {
		a.fail(w, err)
		return
	}
	code := http.StatusAccepted
	if rc.Duplicate {
		code = http.StatusOK
	}
	a.json(w, code, rc)
}

// parseKnown reads the known filter: known=false hides findings that match
// a known issue, known=true lists only those, and absent lists all.
func parseKnown(r *http.Request) (KnownFilter, error) {
	switch v := r.URL.Query().Get("known"); v {
	case "":
		return KnownAll, nil
	case "false":
		return KnownHide, nil
	case "true":
		return KnownOnly, nil
	default:
		return KnownAll, fmt.Errorf("%w: known must be true or false, not %q", ErrBadRequest, v)
	}
}
