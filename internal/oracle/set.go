package oracle

import (
	"context"
	"encoding/hex"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"

	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/httpapi"
)

// Set holds findings deduplicated by ID, in order of first occurrence. It is
// safe for concurrent use, so the engine adds to it while the HTTP API reads.
type Set struct {
	mu   sync.Mutex // protects ids, list
	ids  map[string]bool
	list []Finding
}

// NewSet returns an empty set.
func NewSet() *Set { return &Set{ids: map[string]bool{}} }

// Add keeps every finding whose ID is new and returns those, in order. The
// engine logs exactly what Add returns.
func (s *Set) Add(fs ...Finding) []Finding {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Finding
	for _, f := range fs {
		if s.ids[f.ID] {
			continue
		}
		s.ids[f.ID] = true
		s.list = append(s.list, f)
		out = append(out, f)
	}
	return out
}

// Load adds a logged finding and ignores every other record. It is an
// eventlog.Reader.Scan callback: a restart rebuilds the set from the log, so
// a finding already logged is not logged again.
func (s *Set) Load(r eventlog.Record) error {
	if r.Class != eventlog.Decision || r.Kind != KindFinding {
		return nil
	}
	f, err := Decode(r)
	if err != nil {
		return err
	}
	s.Add(f)
	return nil
}

// Len is the number of distinct findings.
func (s *Set) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.list)
}

// Find returns the first occurrence of the finding with this ID.
func (s *Set) Find(id string) (Finding, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := slices.IndexFunc(s.list, func(f Finding) bool { return f.ID == id })
	if i < 0 {
		return Finding{}, false
	}
	return s.list[i], true
}

// Findings implements httpapi.Source.Findings: one page of findings in order
// of first occurrence.
func (s *Set) Findings(_ context.Context, p httpapi.Page) (httpapi.FindingPage, error) {
	if p.Limit <= 0 {
		p.Limit = httpapi.DefaultLimit
	}
	s.mu.Lock()
	total := len(s.list)
	lo := min(max(p.Offset, 0), total)
	hi := min(lo+p.Limit, total)
	page := slices.Clone(s.list[lo:hi])
	s.mu.Unlock()
	rows := make([]httpapi.Finding, 0, len(page))
	for _, f := range page {
		rows = append(rows, API(f))
	}
	return httpapi.FindingPage{Total: total, Offset: p.Offset, Limit: p.Limit, Rows: rows}, nil
}

// API converts a finding to the HTTP API's shape. The expected verdict set
// is the first verdict, from source "expected"; a monitor finding lists the
// nodes it involves with verdict "involved".
func API(f Finding) httpapi.Finding {
	out := httpapi.Finding{
		ID: f.ID, Seq: f.Seq,
		Summary:      fmt.Sprintf("%s %s: %s", f.Severity, f.Kind, f.Summary),
		TxCBORHex:    hex.EncodeToString(f.Tx),
		Verdicts:     []httpapi.ImplVerdict{},
		Reproducible: f.Reproducible, ReplayCommand: f.Replay,
	}
	if f.Expected != nil {
		out.Verdicts = append(out.Verdicts, httpapi.ImplVerdict{Impl: "expected", Verdict: f.Expected.String()})
	}
	for _, v := range f.Verdicts {
		text := v.Verdict
		if len(v.Classes) > 0 {
			text += " " + classList(v.Classes)
		}
		out.Verdicts = append(out.Verdicts, implVerdict(v.Node, text))
	}
	if len(f.Verdicts) == 0 {
		for _, n := range f.Nodes {
			out.Verdicts = append(out.Verdicts, implVerdict(n, "involved"))
		}
	}
	return out
}

func implVerdict(n Node, verdict string) httpapi.ImplVerdict {
	return httpapi.ImplVerdict{Impl: n.Impl, Verdict: verdict, Build: buildText(n), Dirty: n.Dirty}
}

// buildText is the pool, the image digest and each dependency's revision,
// sorted by dependency.
func buildText(n Node) string {
	var parts []string
	if n.Pool != "" {
		parts = append(parts, "pool="+n.Pool)
	}
	if n.Digest != "" {
		parts = append(parts, n.Digest)
	}
	for _, dep := range slices.Sorted(maps.Keys(n.Revisions)) {
		parts = append(parts, dep+"="+n.Revisions[dep])
	}
	return strings.Join(parts, " ")
}
