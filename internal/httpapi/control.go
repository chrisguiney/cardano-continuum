package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"regexp"
	"sync"

	"github.com/chrisguiney/cardano-continuum/internal/engine"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/httpapi/view"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

// Op is a control operation.
type Op string

// Control operations.
const (
	OpPause      Op = "pause"
	OpResume     Op = "resume"
	OpSetK       Op = "set_k"
	OpSetWeights Op = "set_weights"
	OpInject     Op = "inject"
)

// Control bounds.
const (
	maxK         = 1000
	maxWeight    = 1000.0
	maxWeights   = 64
	maxControls  = 1 << 16
	maxBodyBytes = 64 << 10
)

var (
	reRequestID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	reWeightKey = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
	// reScenario is a scenario name, then optionally a colon and its
	// argument: a pool actor id, or an upgrade's epochs and image
	// reference, which has colons, slashes and an @ of its own.
	reScenario = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}(:[A-Za-z0-9._/:@-]{1,255})?$`)
)

// Control is a request to steer a run, as the API takes it. The log
// carries it as an engine.Control (Input), whose names are the log format.
type Control struct {
	// RequestID makes the request idempotent.
	RequestID string `json:"request_id"`
	// Op is the operation.
	Op Op `json:"op"`
	// K is the amplification factor.
	K uint32 `json:"k,omitempty"`
	// Weights override intent weights by name.
	Weights map[string]float64 `json:"weights,omitempty"`
	// Scenario names a scenario to inject.
	Scenario string `json:"scenario,omitempty"`
}

// Input is c as the body of a Control input record.
func (c Control) Input() engine.Control {
	return engine.Control{
		RequestID: c.RequestID,
		Op:        string(c.Op),
		K:         c.K,
		Weights:   c.Weights,
		Scenario:  c.Scenario,
	}
}

// Receipt says where a control landed. Duplicate is true when the request
// id had been accepted before and nothing new was logged.
type Receipt struct {
	Seq       uint64 `json:"seq"`
	Duplicate bool   `json:"duplicate"`
}

// Controls turns a validated Control into an engine input. Implementations
// are idempotent per request id.
type Controls interface {
	Submit(ctx context.Context, c Control) (Receipt, error)
}

func badf(format string, a ...any) error {
	return fmt.Errorf("%w: %s", view.ErrBadRequest, fmt.Sprintf(format, a...))
}

// ParseControl decodes and validates one control payload. Unknown fields,
// trailing data, and fields that do not belong to the operation are refused.
func ParseControl(r io.Reader) (Control, error) {
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	var c Control
	if err := dec.Decode(&c); err != nil {
		if isTooLarge(err) {
			return Control{}, err
		}
		return Control{}, badf("control payload: %v", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		if err != nil && isTooLarge(err) {
			return Control{}, err
		}
		return Control{}, badf("trailing data after control payload")
	}
	return c, c.validate()
}

// isTooLarge reports a body cut off by http.MaxBytesReader, which the
// handler answers with 413 rather than 400.
func isTooLarge(err error) bool {
	var mbe *http.MaxBytesError
	return errors.As(err, &mbe)
}

func (c Control) validate() error {
	if !reRequestID.MatchString(c.RequestID) {
		return badf("request_id must match %s", reRequestID)
	}
	extra := func(k bool, w bool, s bool) error {
		if (c.K != 0) != k || (c.Weights != nil) != w || (c.Scenario != "") != s {
			return badf("fields do not match op %q", c.Op)
		}
		return nil
	}
	switch c.Op {
	case OpPause, OpResume:
		return extra(false, false, false)
	case OpSetK:
		if err := extra(true, false, false); err != nil {
			return err
		}
		if c.K > maxK {
			return badf("k must be in 1..%d", maxK)
		}
	case OpSetWeights:
		if err := extra(false, true, false); err != nil {
			return err
		}
		return c.validateWeights()
	case OpInject:
		if err := extra(false, false, true); err != nil {
			return err
		}
		if !reScenario.MatchString(c.Scenario) {
			return badf("scenario must match %s", reScenario)
		}
	default:
		return badf("unknown op %q", c.Op)
	}
	return nil
}

func (c Control) validateWeights() error {
	if n := len(c.Weights); n == 0 || n > maxWeights {
		return badf("weights must hold 1..%d entries", maxWeights)
	}
	for k, v := range c.Weights {
		if !reWeightKey.MatchString(k) {
			return badf("weight name %q must match %s", k, reWeightKey)
		}
		if math.IsNaN(v) || v < 0 || v > maxWeight {
			return badf("weight %q must be in 0..%v", k, maxWeight)
		}
	}
	return nil
}

// ControlLog is the log a LogControls appends to.
type ControlLog interface {
	eventlog.Writer
	eventlog.Reader
	// LastSeq is the seq of the last entry.
	LastSeq() uint64
}

// LogControls appends each control to the event log as a Control input.
// It must be the only writer of the log; a log an engine is appending to
// would see interleaved seqs.
type LogControls struct {
	log ControlLog
	at  func() sched.Time

	// mu serialises the duplicate check and the append so one request id is
	// logged once; it is held across the log write on purpose.
	mu   sync.Mutex // protects seen
	seen map[string]uint64
}

// NewLogControls returns controls writing to log, stamping each record with
// at(). It reads the log's earlier Control records so a request id accepted
// before a restart is still a duplicate.
func NewLogControls(ctx context.Context, log ControlLog, at func() sched.Time) (*LogControls, error) {
	c := &LogControls{log: log, at: at, seen: map[string]uint64{}}
	err := log.Scan(ctx, 1, func(r eventlog.Record) error {
		if r.Kind != eventlog.KindControl {
			return nil
		}
		var body engine.Control
		if err := eventlog.Decode(r.Body, &body); err != nil {
			return fmt.Errorf("control record seq %d: %w", r.Seq, err)
		}
		c.seen[body.RequestID] = r.Seq
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("scan controls: %w", err)
	}
	return c, nil
}

// ErrTooManyControls is returned when the log already holds the most
// controls a server will track for idempotency.
var ErrTooManyControls = errors.New("control limit reached")

// Submit implements Controls.
func (l *LogControls) Submit(ctx context.Context, c Control) (Receipt, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if seq, ok := l.seen[c.RequestID]; ok {
		return Receipt{Seq: seq, Duplicate: true}, nil
	}
	if len(l.seen) >= maxControls {
		return Receipt{}, ErrTooManyControls
	}
	body, err := eventlog.Encode(c.Input())
	if err != nil {
		return Receipt{}, fmt.Errorf("encode control %s: %w", c.RequestID, err)
	}
	rec := eventlog.Record{At: l.at(), Class: eventlog.Input, Kind: eventlog.KindControl, Body: body}
	if err := l.log.Append(ctx, rec); err != nil {
		return Receipt{}, fmt.Errorf("append control %s: %w", c.RequestID, err)
	}
	if err := l.log.Sync(ctx); err != nil {
		return Receipt{}, fmt.Errorf("sync control %s: %w", c.RequestID, err)
	}
	seq := l.log.LastSeq()
	l.seen[c.RequestID] = seq
	return Receipt{Seq: seq}, nil
}
