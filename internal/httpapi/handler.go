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

	"github.com/chrisguiney/cardano-continuum/internal/httpapi/view"
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
	// Explorer, when set, serves /api/explorer; without it those paths are
	// 404 and the capabilities say so.
	Explorer *Explorer
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
//	GET  /api/meta/enums
//	GET  /api/overview
//	GET  /api/controls
//	GET  /api/actors?limit&offset&sort&dir&profile&state&role&kind&delegated
//	GET  /api/actors/{id}
//	GET  /api/adversaries
//	GET  /api/actors/{id}/timeline
//	GET  /api/nodes
//	GET  /api/pools
//	GET  /api/governance
//	GET  /api/governance/{tx}/{index}
//	GET  /api/findings?limit&offset&known=true|false&kind&severity
//	GET  /api/findings/{id}
//	GET  /api/decisions?actor&profile&kind&intent&vote&last_event_id   (SSE)
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
		a.json(w, http.StatusOK, map[string]bool{"controls": a.Controls != nil, "explorer": a.Explorer != nil})
	})
	mux.HandleFunc("GET /api/meta/enums", func(w http.ResponseWriter, _ *http.Request) {
		a.json(w, http.StatusOK, view.BuildEnums())
	})
	mux.HandleFunc("GET /api/overview", a.overview)
	mux.HandleFunc("GET /api/controls", a.controls)
	mux.HandleFunc("GET /api/actors", a.actors)
	mux.HandleFunc("GET /api/actors/{id}/timeline", a.timeline)
	mux.HandleFunc("GET /api/actors/{id}", a.actorDetail)
	mux.HandleFunc("GET /api/adversaries", a.adversaries)
	mux.HandleFunc("GET /api/nodes", a.nodes)
	mux.HandleFunc("GET /api/pools", a.stakePools)
	mux.HandleFunc("GET /api/governance", a.governance)
	mux.HandleFunc("GET /api/findings", a.findings)
	mux.HandleFunc("GET /api/findings/{id}", func(w http.ResponseWriter, _ *http.Request) {
		a.fail(w, fmt.Errorf("%w: finding", view.ErrNotFound))
	})
	mux.HandleFunc("GET /api/decisions", sseFeed{a}.serve)
	mux.HandleFunc("POST /api/control", a.control)
	if a.Explorer != nil {
		exRoutes{a}.register(mux)
	}
	govRoutes{a}.register(mux)
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
	case errors.Is(err, view.ErrBadRequest):
		a.json(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
	case errors.Is(err, view.ErrNotFound):
		a.json(w, http.StatusNotFound, map[string]string{"error": err.Error()})
	case errors.Is(err, view.ErrTooManyClients):
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

// controls serves the applied steering whether or not this server takes
// controls: a read-only server still shows how the run was steered.
func (a *api) controls(w http.ResponseWriter, r *http.Request) {
	v, err := a.Source.Controls(r.Context())
	if err != nil {
		a.fail(w, err)
		return
	}
	a.json(w, http.StatusOK, v)
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

// nodesView is the Nodes view, with the explorer resolving the keys of
// the nodes the source could not, and counting the blocks each forged and
// verified on the explorer's chain.
func (a *api) nodesView(r *http.Request) (view.NodesView, error) {
	v, err := a.Source.Nodes(r.Context())
	if err != nil {
		return view.NodesView{}, err
	}
	if a.Explorer != nil {
		v = view.WithCounts(withKeys(a.Explorer.idx, v), a.Explorer.idx.Counts)
	}
	return v, nil
}

func (a *api) nodes(w http.ResponseWriter, r *http.Request) {
	v, err := a.nodesView(r)
	if err != nil {
		a.fail(w, err)
		return
	}
	a.json(w, http.StatusOK, v)
}

// stakePools serves the on-chain pools: the pool actors, what the chain
// shows of them, and the nodes that run their keys.
func (a *api) stakePools(w http.ResponseWriter, r *http.Request) {
	v, err := a.Source.StakePools(r.Context())
	if err != nil {
		a.fail(w, err)
		return
	}

	nodes, err := a.nodesView(r)
	if err != nil {
		a.fail(w, err)
		return
	}

	if a.Explorer != nil {
		v = view.WithChain(v, a.Explorer.idx.Pools())
	}
	a.json(w, http.StatusOK, view.WithNodes(v, nodes))
}

func (a *api) governance(w http.ResponseWriter, r *http.Request) {
	g, err := a.Source.Governance(r.Context())
	if err != nil {
		a.fail(w, err)
		return
	}
	if a.Explorer != nil {
		fillSummaries(a.Explorer, &g)
	}
	a.json(w, http.StatusOK, g)
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
	ff, err := parseFindingFilter(r)
	if err != nil {
		a.fail(w, err)
		return
	}
	f, err := a.Source.Findings(r.Context(), view.Page{Limit: q.Limit, Offset: q.Offset, Known: known, Kind: ff.Kind, Severity: ff.Severity})
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
	c, err := ParseControl(http.MaxBytesReader(w, r.Body, MaxBodyBytes))
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
func parseKnown(r *http.Request) (view.KnownFilter, error) {
	switch v := r.URL.Query().Get("known"); v {
	case "":
		return view.KnownAll, nil
	case "false":
		return view.KnownHide, nil
	case "true":
		return view.KnownOnly, nil
	default:
		return view.KnownAll, fmt.Errorf("%w: known must be true or false, not %q", view.ErrBadRequest, v)
	}
}
