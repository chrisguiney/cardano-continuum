package oracle

import (
	"errors"
	"fmt"

	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

// KindFinding is the record kind of a finding. It is a Decision: findings
// are derived from the inputs before it, so replay regenerates each one and
// compares it with the logged record.
const KindFinding = "Finding"

// ErrNotFinding reports a record of another kind given to Decode.
var ErrNotFinding = errors.New("oracle: record is not a finding")

// Record returns f as a log record at simulated time at. The body is f in
// the log's canonical CBOR, so equal findings give equal bytes.
func (f Finding) Record(at sched.Time) (eventlog.Record, error) {
	b, err := eventlog.Encode(f)
	if err != nil {
		return eventlog.Record{}, fmt.Errorf("oracle: encode finding %s: %w", f.ID, err)
	}
	return eventlog.Record{At: at, Class: eventlog.Decision, Kind: KindFinding, Body: b}, nil
}

// Decode reads a finding record.
func Decode(r eventlog.Record) (Finding, error) {
	if r.Class != eventlog.Decision || r.Kind != KindFinding {
		return Finding{}, fmt.Errorf("%w: seq %d is %s", ErrNotFinding, r.Seq, r.Kind)
	}
	var f Finding
	if err := eventlog.Decode(r.Body, &f); err != nil {
		return Finding{}, fmt.Errorf("oracle: decode finding at seq %d: %w", r.Seq, err)
	}
	return f, nil
}
