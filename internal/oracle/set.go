package oracle

import (
	"slices"
	"sync"

	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
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

// Page returns the findings from offset, at most limit of them, in order of
// first occurrence, and how many there are in all.
func (s *Set) Page(offset, limit int) ([]Finding, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	total := len(s.list)
	lo := min(max(offset, 0), total)
	hi := min(lo+max(limit, 0), total)
	return slices.Clone(s.list[lo:hi]), total
}
