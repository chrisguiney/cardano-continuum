package judge

import (
	"github.com/chrisguiney/cardano-continuum/internal/oracle"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

// Log is the findings logged so far.
type Log struct {
	set *oracle.Set
	jr  Journal
}

// Set is the set of findings this judge has logged. It is safe for
// concurrent use, so an API may serve it while the loop runs.
func (l *Log) Set() *oracle.Set { return l.set }

// Use logs into set from now on.
func (l *Log) Use(set *oracle.Set) { l.set = set }

// Report logs each finding the set has not seen.
func (l *Log) Report(at sched.Time, fs []oracle.Finding) error {
	for _, f := range l.set.Add(fs...) {
		rec, err := f.Record(at)
		if err != nil {
			return err
		}
		l.jr.Append(rec)
	}
	return nil
}
