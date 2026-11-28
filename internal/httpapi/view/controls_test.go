package view

import (
	"reflect"
	"testing"

	"github.com/chrisguiney/cardano-continuum/internal/engine"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/keys"
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
	if got := controlsOf(l.p); got.K != 1 || got.Paused || len(got.Weights) != 0 || got.Weights == nil || got.Last != nil {
		t.Fatalf("empty log: %+v", got)
	}

	l.control(engine.Control{RequestID: "a", Op: "set_k", K: 4})
	if got := controlsOf(l.p); got.K != 1 || got.Last != nil {
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
	if got := controlsOf(l.p); !reflect.DeepEqual(got, want) {
		t.Fatalf("applied: %+v, want %+v", got, want)
	}

	l.control(engine.Control{RequestID: "d", Op: "set_weights", Weights: map[string]float64{"mint": 3}})
	l.answer("d", "set_weights", "")
	if got := controlsOf(l.p).Weights; !reflect.DeepEqual(got, map[string]float64{"mint": 3}) {
		t.Fatalf("set_weights replaces the override: %v", got)
	}

	l.control(engine.Control{RequestID: "e", Op: "set_k"})
	l.answer("e", "set_k", "k must be positive")
	if got := controlsOf(l.p); got.K != 4 || got.Last.Why != "k must be positive" || got.Last.RequestID != "e" {
		t.Fatalf("refused control: %+v", got)
	}

	l.control(engine.Control{RequestID: "f", Op: "clear_weights"})
	l.answer("f", "clear_weights", "")
	l.control(engine.Control{RequestID: "g", Op: "resume"})
	l.answer("g", "resume", "")
	if got := controlsOf(l.p); got.Paused || len(got.Weights) != 0 || got.Weights == nil || got.K != 4 {
		t.Fatalf("cleared and resumed: %+v", got)
	}
}

// TestControlStateIsACopy: a caller changing the returned weights does not
// change the projection.
func TestControlStateIsACopy(t *testing.T) {
	l := &controlLog{t: t, p: NewProjection()}
	l.control(engine.Control{RequestID: "a", Op: "set_weights", Weights: map[string]float64{"send": 2}})
	l.answer("a", "set_weights", "")
	controlsOf(l.p).Weights["send"] = 9
	if got := controlsOf(l.p).Weights["send"]; got != 2 {
		t.Fatalf("weights aliased: %v", got)
	}
}

// Engine-wide records (control answers, contract deployments, incidents)
// are logged under actor 0 and the faucet's payments under the faucet's
// account. Neither is a member of the population: the overview counted
// them as two actors of profile "unknown".
func TestSystemRecordsAreNotActors(t *testing.T) {
	t.Parallel()
	l := &controlLog{t: t, p: NewProjection()}
	l.answer("r1", "pause", "")
	l.add(eventlog.Decision, engine.KindDeployed, engine.Deployed{Contract: 0, Address: "lock"})
	l.add(eventlog.Decision, engine.KindIncident, engine.Incident{Kind: "stall"})

	b, err := eventlog.Encode(engine.Submitted{Intent: 1, To: 5, Amount: 10})
	if err != nil {
		t.Fatal(err)
	}
	l.seq++
	l.p.Apply(eventlog.Record{Seq: l.seq, Class: eventlog.Decision, Kind: engine.KindSubmitted, Actor: eventlog.ActorID(keys.FaucetID), Body: b})

	counts := l.p.ProfileCounts(func(uint64) string { return "" })
	if counts[UnknownProfile] != 0 || len(counts) != 0 {
		t.Fatalf("profile counts %v: want no population members", counts)
	}

	if s := l.p.Snapshot(nil); s.Population.Total != 0 {
		t.Fatalf("population total %d, want 0", s.Population.Total)
	}

	rows := l.p.Rows()
	if len(rows) != 1 || rows[0].ID != uint64(keys.FaucetID) || rows[0].Profile != FaucetProfile {
		t.Fatalf("rows %+v: want only the faucet, labelled %q", rows, FaucetProfile)
	}
}

// The Markets view is the projection's fold of the market records: Quote
// points, Traded flows, births by currency. A Quote is no actor's.
func TestMarketsView(t *testing.T) {
	p := NewProjection()
	add := func(seq uint64, kind string, actor uint64, body any) {
		p.Apply(eventlog.Record{Seq: seq, At: 60_000, Class: eventlog.Decision, Kind: kind, Actor: eventlog.ActorID(actor), Body: mustEncode(t, body)})
	}
	add(1, engine.KindQuote, 0, engine.Quote{ADA: 0.5, Mids: []engine.Mid{{Currency: "USD", Mid: 0.5}}})
	add(2, engine.KindSpawned, 3, engine.Spawned{Profile: "p", Currency: "USD"})
	add(3, engine.KindTraded, 3, engine.Traded{Side: "buy", Exchange: "north", Currency: "USD", Lovelace: 5, Fiat: 2.5})
	m := marketsOf(p)
	if len(m.Quotes) != 1 || m.Births["USD"] != 1 || len(m.Flows) != 1 || m.Flows[0].Fiat != 2.5 {
		t.Fatalf("markets view %+v", m)
	}
	if n := p.Snapshot(nil).Population.Total; n != 1 {
		t.Fatalf("population %d: a Quote made an actor", n)
	}
}
