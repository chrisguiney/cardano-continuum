package view

import (
	"reflect"
	"testing"

	"github.com/chrisguiney/cardano-continuum/internal/engine"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
)

// controlLog appends Control inputs and the engine's Controlled answers to
// a projection, numbering records from 1.
type controlLog struct {
	t *testing.T
	p *Projection
	// seq is the last record's seq.
	seq uint64
}

func (l *controlLog) add(class eventlog.Class, kind string, body any) {
	l.t.Helper()
	b, err := eventlog.Encode(body)
	if err != nil {
		l.t.Fatal(err)
	}
	l.seq++
	l.p.Apply(eventlog.Record{
		Seq:   l.seq,
		At:    0,
		Class: class,
		Kind:  kind,
		Body:  b,
	})
}

func (l *controlLog) control(c engine.Control) {
	l.add(eventlog.Input, eventlog.KindControl, c)
}

func (l *controlLog) answer(id, op, why string) {
	l.add(eventlog.Decision, engine.KindControlled, engine.Controlled{
		RequestID: id,
		Op:        op,
		Why:       why,
	})
}

// TestControlStateFollowsAppliedControls: the state is what the engine
// applied, not what was asked: a control the engine has not answered yet,
// or answered with a reason, changes nothing.
func TestControlStateFollowsAppliedControls(t *testing.T) {
	l := &controlLog{t: t, p: NewProjection()}
	if got := l.p.Controls(); got.K != 1 || got.Paused || len(got.Weights) != 0 || got.Weights == nil || got.Last != nil {
		t.Fatalf("empty log: %+v", got)
	}

	l.control(engine.Control{RequestID: "a", Op: "set_k", K: 4})
	if got := l.p.Controls(); got.K != 1 || got.Last != nil {
		t.Fatalf("before the engine answers: %+v", got)
	}

	l.answer("a", "set_k", "")
	l.control(engine.Control{RequestID: "b", Op: "pause"})
	l.answer("b", "pause", "")
	l.control(engine.Control{RequestID: "c", Op: "set_weights", Weights: map[string]float64{"send": 2, "vote": 0}})
	l.answer("c", "set_weights", "")
	want := ControlState{
		Paused:  true,
		K:       4,
		Weights: map[string]float64{"send": 2, "vote": 0},
		Last: &LastControl{
			RequestID: "c",
			Op:        "set_weights",
			Seq:       5,
		},
	}
	if got := l.p.Controls(); !reflect.DeepEqual(got, want) {
		t.Fatalf("applied: %+v, want %+v", got, want)
	}

	l.control(engine.Control{RequestID: "d", Op: "set_weights", Weights: map[string]float64{"mint": 3}})
	l.answer("d", "set_weights", "")
	if got := l.p.Controls().Weights; !reflect.DeepEqual(got, map[string]float64{"mint": 3}) {
		t.Fatalf("set_weights replaces the override: %v", got)
	}

	l.control(engine.Control{RequestID: "e", Op: "set_k"})
	l.answer("e", "set_k", "k must be positive")
	if got := l.p.Controls(); got.K != 4 || got.Last.Why != "k must be positive" || got.Last.RequestID != "e" {
		t.Fatalf("refused control: %+v", got)
	}

	l.control(engine.Control{RequestID: "f", Op: "clear_weights"})
	l.answer("f", "clear_weights", "")
	l.control(engine.Control{RequestID: "g", Op: "resume"})
	l.answer("g", "resume", "")
	if got := l.p.Controls(); got.Paused || len(got.Weights) != 0 || got.Weights == nil || got.K != 4 {
		t.Fatalf("cleared and resumed: %+v", got)
	}
}

// TestControlStateIsACopy: a caller changing the returned weights does not
// change the projection.
func TestControlStateIsACopy(t *testing.T) {
	l := &controlLog{t: t, p: NewProjection()}
	l.control(engine.Control{RequestID: "a", Op: "set_weights", Weights: map[string]float64{"send": 2}})
	l.answer("a", "set_weights", "")
	l.p.Controls().Weights["send"] = 9
	if got := l.p.Controls().Weights["send"]; got != 2 {
		t.Fatalf("weights aliased: %v", got)
	}
}
