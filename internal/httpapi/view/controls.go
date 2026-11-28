package view

import (
	stdmaps "maps"

	"github.com/chrisguiney/cardano-continuum/internal/engine"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
)

// ControlState is the Controls view: the steering the engine has applied.
// It is folded from the log, so it survives a restart of the engine or of
// the API and a replay arrives at the same state. A control counts once
// the engine's Controlled decision answers it without a reason, the same
// rule the engine's steer component applies.
type ControlState struct {
	// Paused reports the agendas stopped.
	Paused bool `json:"paused"`
	// K is the rate multiplier in force; 1 when none was set.
	K uint32 `json:"k"`
	// Weights is the intent weight override by kind; empty when the
	// profiles' own weights apply.
	Weights map[string]float64 `json:"weights"`
	// Last is the newest control the engine answered; nil before any.
	Last *LastControl `json:"last"`
}

// LastControl is a control the engine answered.
type LastControl struct {
	// RequestID is the control's idempotency key.
	RequestID string `json:"request_id"`
	// Op is the operation.
	Op string `json:"op"`
	// Seq is the log seq of the Control input, the seq its receipt gave.
	Seq uint64 `json:"seq"`
	// At is when the engine answered, in simulated milliseconds.
	At int64 `json:"at"`
	// Why is the engine's reason for not applying it; empty when applied.
	Why string `json:"why,omitempty"`
}

// pendingControl is a Control input waiting for the engine's answer.
type pendingControl struct {
	// body is the input as logged.
	body engine.Control
	// seq is the input's log seq.
	seq uint64
}

// controlFold folds Control inputs and Controlled decisions. The caller
// holds the projection's lock.
type controlFold struct {
	// pending are the inputs not yet answered, by request id. The writers
	// cap how many controls a log holds, which bounds it.
	pending map[string]pendingControl
	// state is the applied steering; state.K is zero until a set_k.
	state ControlState
}

func newControlFold() *controlFold {
	return &controlFold{pending: map[string]pendingControl{}}
}

func (f *controlFold) input(r eventlog.Record) error {
	var c engine.Control
	if err := eventlog.Decode(r.Body, &c); err != nil {
		return err
	}
	f.pending[c.RequestID] = pendingControl{body: c, seq: r.Seq}
	return nil
}

func (f *controlFold) answer(r eventlog.Record) error {
	var a engine.Controlled
	if err := eventlog.Decode(r.Body, &a); err != nil {
		return err
	}
	p, ok := f.pending[a.RequestID]
	delete(f.pending, a.RequestID)
	f.state.Last = &LastControl{
		RequestID: a.RequestID,
		Op:        a.Op,
		Seq:       p.seq,
		At:        int64(r.At),
		Why:       a.Why,
	}
	if ok && a.Why == "" {
		f.apply(p.body)
	}
	return nil
}

// apply mirrors steer.Control for the operations that change state.
func (f *controlFold) apply(c engine.Control) {
	switch c.Op {
	case "pause":
		f.state.Paused = true
	case "resume":
		f.state.Paused = false
	case "set_k":
		f.state.K = c.K
	case "set_weights":
		f.state.Weights = stdmaps.Clone(c.Weights)
	case "clear_weights":
		f.state.Weights = nil
	}
}

// view is a copy of the state with K and Weights defaulted.
func (f *controlFold) view() ControlState {
	v := f.state
	v.K = max(v.K, 1)
	v.Weights = stdmaps.Clone(v.Weights)
	if v.Weights == nil {
		v.Weights = map[string]float64{}
	}
	if v.Last != nil {
		last := *v.Last
		v.Last = &last
	}
	return v
}
