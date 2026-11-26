package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// sseFeed serves the decision feed over the API it is mounted on.
type sseFeed struct{ *api }

// serve streams the feed as server-sent events. Each frame's id is the
// decision's log seq, so a reconnecting browser resumes through
// Last-Event-ID. The stream ends when the client leaves, the server shuts
// down, or the hub drops the client for falling behind; EventSource then
// reconnects with its last id.
func (a sseFeed) serve(w http.ResponseWriter, r *http.Request) {
	q, err := queryOf(r)
	if err != nil {
		a.fail(w, err)
		return
	}
	f, err := parseFilter(q)
	if err != nil {
		a.fail(w, err)
		return
	}
	after, err := parseAfter(r.Header.Get("Last-Event-ID"), q)
	if err != nil {
		a.fail(w, err)
		return
	}
	sub, err := a.Source.Subscribe(f, after)
	if err != nil {
		a.fail(w, err)
		return
	}
	defer sub.Close()

	rc := http.NewResponseController(w)
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	s := &sseWriter{w: w, rc: rc, timeout: a.WriteTimeout}
	if err := s.flush(); err != nil {
		return
	}
	if sub.Gap {
		if err := s.frame("", "gap", []byte(`{}`)); err != nil {
			return
		}
	}
	for _, d := range sub.Replay {
		if err := s.decision(d); err != nil {
			return
		}
	}
	a.pump(r, sub, s)
}

func (a sseFeed) pump(r *http.Request, sub *Subscription, s *sseWriter) {
	beat := time.NewTicker(a.Heartbeat)
	defer beat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case d, ok := <-sub.C:
			if !ok {
				a.Logger.Info("feed client disconnected", "remote", r.RemoteAddr)
				return
			}
			if err := s.decision(d); err != nil {
				return
			}
		case <-beat.C:
			if err := s.write(": keepalive\n\n"); err != nil {
				return
			}
		}
	}
}

// sseWriter writes SSE frames with a deadline on each write.
type sseWriter struct {
	w       http.ResponseWriter
	rc      *http.ResponseController
	timeout time.Duration
}

func (s *sseWriter) write(text string) error {
	_ = s.rc.SetWriteDeadline(time.Now().Add(s.timeout)) // unsupported on some writers; the client then bounds the write
	if _, err := fmt.Fprint(s.w, text); err != nil {
		return err
	}
	return s.flush()
}

func (s *sseWriter) flush() error {
	_ = s.rc.SetWriteDeadline(time.Now().Add(s.timeout))
	return s.rc.Flush()
}

func (s *sseWriter) frame(id, event string, data []byte) error {
	text := ""
	if id != "" {
		text = "id: " + id + "\n"
	}
	return s.write(text + "event: " + event + "\ndata: " + string(data) + "\n\n")
}

func (s *sseWriter) decision(d Decision) error {
	b, err := json.Marshal(d)
	if err != nil {
		return err
	}
	return s.frame(strconv.FormatUint(d.ID, 10), "decision", b)
}
