package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"
)

const (
	// statusPath is the proxy's own status route.
	statusPath = "/api/_ui/status"
	// probePath is the engine route the prober asks: small, and answered
	// from memory once the API is serving.
	probePath = "/api/capabilities"
	// probeTimeout bounds one probe, so a hung engine reads as down.
	probeTimeout = 2 * time.Second
	// probeTTL is how long a probe's answer is reused, so a UI polling
	// every few seconds costs the engine one request per TTL.
	probeTTL = 3 * time.Second
)

// Status is the engine's reachability as the UI proxy sees it.
type Status struct {
	// Engine is "up" once the engine API answers, "starting" while it has
	// not answered since the proxy began, and "unreachable" when it
	// answered before and no longer does.
	Engine string `json:"engine"`
	// Since is when Engine last changed.
	Since time.Time `json:"since"`
	// Now is the proxy's clock when the status was read, so a browser can
	// measure how long the engine has been in this state without trusting
	// its own clock against the proxy's.
	Now time.Time `json:"now"`
	// LastError says why the latest probe failed; empty when Engine is up.
	LastError string `json:"last_error,omitempty"`
}

// prober probes the engine API and caches the answer for ttl.
type prober struct {
	// api is the engine's base URL.
	api *url.URL
	// client sends the probes.
	client *http.Client
	// ttl is how long an answer is reused; zero probes on every call.
	ttl time.Duration
	// now reads the clock.
	now func() time.Time

	mu sync.Mutex // protects st, checked, seen
	// st is the latest status.
	st Status
	// checked is when st was probed.
	checked time.Time
	// seen is whether the engine has ever answered.
	seen bool
}

func newProber(api *url.URL, ttl time.Duration, now func() time.Time) *prober {
	return &prober{
		api:    api,
		client: &http.Client{Timeout: probeTimeout},
		ttl:    ttl,
		now:    now,
		st:     Status{Engine: "starting", Since: now()},
	}
}

// Status returns the cached status, probing the engine first when the cache
// is older than the TTL. The probe runs outside the lock: callers that
// race each probe once, which the engine tolerates.
func (p *prober) Status(ctx context.Context) Status {
	p.mu.Lock()
	if !p.checked.IsZero() && p.now().Sub(p.checked) < p.ttl {
		st := p.st
		p.mu.Unlock()
		st.Now = p.now()
		return st
	}
	p.mu.Unlock()

	err := p.probe(ctx)

	p.mu.Lock()
	defer p.mu.Unlock()
	p.checked = p.now()
	engine, msg := "up", ""
	switch {
	case err != nil && p.seen:
		engine, msg = "unreachable", err.Error()
	case err != nil:
		engine, msg = "starting", err.Error()
	default:
		p.seen = true
	}
	if engine != p.st.Engine {
		p.st.Since = p.checked
	}
	p.st.Engine, p.st.LastError = engine, msg
	st := p.st
	st.Now = p.checked
	return st
}

// probe asks the engine one cheap question. Any answer below 500 means the
// API is serving, including a 404 from an engine without the route.
func (p *prober) probe(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.api.JoinPath(probePath).String(), nil)
	if err != nil {
		return fmt.Errorf("build probe: %w", err)
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("probe engine api: %w", err)
	}

	_ = resp.Body.Close()
	if resp.StatusCode >= http.StatusInternalServerError {
		return fmt.Errorf("engine api answered %d", resp.StatusCode)
	}
	return nil
}

// writeStatus answers the status route with the prober's current status.
func writeStatus(w http.ResponseWriter, r *http.Request, p *prober) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(p.Status(r.Context()))
}
