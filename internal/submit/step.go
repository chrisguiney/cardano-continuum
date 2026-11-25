package submit

import (
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"time"

	"github.com/chrisguiney/cardano-continuum/internal/chain"
	"github.com/chrisguiney/cardano-continuum/internal/population"
)

// This file is the retry core: the pure state machine that decides what an
// actor does next about one payment. Its rules are the RETRY STATE MACHINE
// CONTRACT in models/specs/submit-retry/README.md, model-checked in
// SubmitRetry.tla.
//
// The machine never releases a reservation. Every attempt is built through
// plan.Ledger.Reserve, which keeps it unable to land beside an unfinished
// earlier one. The only thing the machine asks the ledger to write off is an
// attempt a node's answer proves never reached a mempool (Refused), decided
// from the answer by Outcome.Refutes and never from the actor's diagnosis.
//
//	```mermaid
//	stateDiagram-v2
//	  [*] --> Build
//	  Build --> Submit: Built
//	  Build --> GaveUp: BuildFailed, patience spent
//	  Submit --> Pending: accepted
//	  Submit --> Submit: unreached, resend
//	  Submit --> Diagnosing: rejected
//	  Rejected --> Build: Diagnosed
//	  Pending --> Confirmed: InBlock
//	  Pending --> Build: Dropped
//	  Confirmed --> Submit: LeftView, resend
//	  Submit --> Build: Dead
//	  Pending --> Build: Dead
//	  Confirmed --> Build: Dead
//	  Build --> Done: Settled
//	```
//
// Settled reaches Done from every state but GaveUp, where it only marks the
// intent paid. Done and GaveUp are final.

// ErrIllegal is returned by Step for an event the state has no row for.
var ErrIllegal = errors.New("submit: event not legal in this state")

// State is where a payment is in the retry loop.
type State uint8

// States. The zero value is Build, so a fresh Machine is ready to Start.
const (
	// Build: waiting for the caller to build the next attempt.
	Build State = iota
	// Submit: an attempt is being sent, or is waiting to be sent again.
	Submit
	// Pending: a node accepted the attempt; it is not yet in a block.
	Pending
	// Confirmed: the planner's view has an attempt in a block, not yet settled.
	Confirmed
	// Diagnosing: every target rejected the attempt; waiting for a diagnosis.
	Diagnosing
	// Done: an attempt settled.
	Done
	// GaveUp: patience is spent. Final, and it releases nothing.
	GaveUp
)

func (s State) String() string {
	names := [...]string{"Build", "Submit", "Pending", "Confirmed", "Diagnosing", "Done", "GaveUp"}
	if int(s) < len(names) {
		return names[s]
	}
	return fmt.Sprintf("State(%d)", s)
}

// Machine is one payment's retry state. It is persisted with the payment.
type Machine struct {
	State State `cbor:"1,keyasint"`
	// Current is the attempt being worked on.
	Current chain.TxID `cbor:"2,keyasint"`
	// Attempts counts every send, first or again. It is never reset: not by
	// a rollback, a drop or a restart.
	Attempts uint32 `cbor:"3,keyasint"`
	// Fixed holds the classes the actor has fixed, sorted, without repeats.
	// It only grows; a fix never undoes another.
	Fixed []Class `cbor:"4,keyasint,omitempty"`
	// Sent is true once a send of Current returned Accepted or Unreached,
	// and for every unfinished attempt after a restart. It makes a later
	// "inputs spent" answer ambiguous: the attempt may have caused it.
	Sent bool `cbor:"5,keyasint,omitempty"`
	// Paid is set when an attempt settled after the actor gave up.
	Paid bool `cbor:"6,keyasint,omitempty"`
}

// Fixes reports whether c has been fixed.
func (m Machine) Fixes(c Class) bool { return slices.Contains(m.Fixed, c) }

// Event is something the caller tells the machine. The caller does the I/O:
// building, sending, reading the ledger and chain view.
type Event interface{ isEvent() }

// Events.
type (
	// Start begins, or resumes, the loop in Build: the caller builds an
	// attempt through Reserve.
	Start struct{}
	// Built reports that Reserve accepted attempt Tx.
	Built struct{ Tx chain.TxID }
	// BuildFailed reports that Reserve refused the rebuild. The machine
	// fails closed: it gives up rather than build around the ledger.
	BuildFailed struct{}
	// Answered is the node's answer to the last SendTx, aggregated over every
	// target with Aggregate.
	Answered struct{ Out Outcome }
	// Diagnosed is the actor's reading of a rejection: the class it believes
	// it got wrong. It chooses the fix and nothing else.
	Diagnosed struct{ Class Class }
	// InBlock reports that the planner's view holds an attempt of the intent
	// in a block.
	InBlock struct{ Tx chain.TxID }
	// LeftView reports that the confirmed attempt left the view: a rollback.
	LeftView struct{}
	// Dropped is a drop notice for the pending attempt: absent from the
	// mempool and from the view, which is also what a block in flight
	// looks like.
	Dropped struct{}
	// Dead reports that the planner wrote the current attempt off (settled
	// expiry or settled conflict).
	Dead struct{}
	// Settled reports that an attempt of the intent settled: it is paid.
	Settled struct{ Tx chain.TxID }
)

func (Start) isEvent()       {}
func (Built) isEvent()       {}
func (BuildFailed) isEvent() {}
func (Answered) isEvent()    {}
func (Diagnosed) isEvent()   {}
func (InBlock) isEvent()     {}
func (LeftView) isEvent()    {}
func (Dropped) isEvent()     {}
func (Dead) isEvent()        {}
func (Settled) isEvent()     {}

// Action is something the machine asks the caller to do. There is no
// release action: the planner releases, on settlement or death.
type Action interface{ isAction() }

// Actions.
type (
	// BuildAttempt asks the caller to build the next attempt through
	// plan.Ledger.Reserve and answer with Built or BuildFailed.
	BuildAttempt struct{}
	// SendTx submits the attempt's bytes to every target.
	SendTx struct{ Tx chain.TxID }
	// Refused asks the caller to call plan.Ledger.Refused: every target
	// rejected Tx, and the answer proves it never reached a mempool.
	Refused struct{ Tx chain.TxID }
	// Diagnose asks the actor to read the rejection and answer Diagnosed.
	Diagnose struct{ Failures []Failure }
	// Backoff delays the next Build or SendTx by D.
	Backoff struct{ D time.Duration }
	// GiveUp records that patience is spent.
	GiveUp struct{}
	// Learn raises the actor's competence: a fix worked.
	Learn struct{}
	// Frustrate raises the actor's frustration: it gave up.
	Frustrate struct{}
)

func (BuildAttempt) isAction() {}
func (SendTx) isAction()       {}
func (Refused) isAction()      {}
func (Diagnose) isAction()     {}
func (Backoff) isAction()      {}
func (GiveUp) isAction()       {}
func (Learn) isAction()        {}
func (Frustrate) isAction()    {}

// Policy shapes the waits between attempts. The model has no time; a backoff
// is the wait between a rejection or an unanswered send and the next attempt.
type Policy struct {
	// Base is the wait before the second attempt; it doubles each attempt.
	Base time.Duration `cbor:"1,keyasint"`
	// Cap bounds every wait, jitter included.
	Cap time.Duration `cbor:"2,keyasint"`
	// Jitter is the fraction a wait varies by, either way, in [0, 1).
	Jitter float64 `cbor:"3,keyasint"`
}

// DefaultPolicy waits 2s, 4s, 8s, ... up to a minute, give or take a fifth.
var DefaultPolicy = Policy{Base: 2 * time.Second, Cap: time.Minute, Jitter: 0.2}

// Backoff is the wait after the n-th attempt (n >= 1): Base doubled n-1
// times, jittered from r, never more than Cap.
func (p Policy) Backoff(n int, r *rand.Rand) time.Duration {
	d := p.Base
	for i := 1; i < n && d < p.Cap; i++ {
		d *= 2
	}
	d = min(d, p.Cap)
	if p.Jitter > 0 && r != nil {
		d = time.Duration(float64(d) * (1 + p.Jitter*(2*r.Float64()-1)))
	}
	return max(min(d, p.Cap), 0)
}

// Patience is the number of sends the actor makes before giving up: its
// trait rounded, and at least one.
func Patience(a population.Actor) int {
	p := a.Traits.Patience
	if math.IsNaN(p) || p < 1 {
		return 1
	}
	return int(min(math.Round(p), math.MaxInt32))
}

// Diagnoser reads a rejection and names the class the actor thinks it got
// wrong. The seam is for the mistake-aware machine: a low-competence actor
// may name any class. Whatever it names changes the fix and nothing else;
// the decision to refuse never reads it.
type Diagnoser interface {
	Diagnose(r *rand.Rand, failures []Failure) Class
}

// Perfect always names the first class the node reported.
type Perfect struct{}

// Diagnose implements Diagnoser.
func (Perfect) Diagnose(_ *rand.Rand, failures []Failure) Class {
	if len(failures) == 0 {
		return Unknown
	}
	return failures[0].Class
}

// Step applies one event. It is pure: the caller does every action. An event
// the state has no row for returns ErrIllegal and leaves the machine as it
// was. Done and GaveUp ignore every event, except that Settled marks a
// GaveUp machine Paid.
func Step(p Policy, m Machine, ev Event, a population.Actor, r *rand.Rand) (Machine, []Action, error) {
	if m.State == Done {
		return m, nil, nil
	}
	if m.State == GaveUp {
		if _, ok := ev.(Settled); ok {
			m.Paid = true
		}
		return m, nil, nil
	}
	if _, ok := ev.(Settled); ok {
		return settle(m), learn(m), nil
	}
	next, acts, ok := row(p, m, ev, a, r)
	if !ok {
		return m, nil, fmt.Errorf("%w: %T in %v", ErrIllegal, ev, m.State)
	}
	return next, acts, nil
}

func settle(m Machine) Machine {
	m.State = Done
	return m
}

func learn(m Machine) []Action {
	if len(m.Fixed) == 0 {
		return nil
	}
	return []Action{Learn{}}
}

// row is the transition table for the live states.
func row(p Policy, m Machine, ev Event, a population.Actor, r *rand.Rand) (Machine, []Action, bool) {
	switch m.State {
	case Build:
		switch ev := ev.(type) {
		case Start:
			next, acts := toBuild(p, m, a, r)
			return next, acts, true
		case Built:
			m.Current, m.Sent = ev.Tx, false
			next, acts := toSend(p, m, a, r)
			return next, acts, true
		case BuildFailed:
			next, acts := giveUp(m)
			return next, acts, true
		case Dead:
			return m, nil, true
		}
	case Submit:
		switch ev := ev.(type) {
		case Answered:
			next, acts := answered(p, m, ev.Out, a, r)
			return next, acts, true
		case Dead:
			next, acts := toBuild(p, m, a, r)
			return next, acts, true
		}
	case Pending:
		switch ev := ev.(type) {
		case InBlock:
			m.State, m.Current = Confirmed, ev.Tx
			return m, nil, true
		case Dropped, Dead:
			next, acts := toBuild(p, m, a, r)
			return next, acts, true
		}
	case Confirmed:
		switch ev.(type) {
		case LeftView:
			next, acts := toSend(p, m, a, r)
			return next, acts, true
		case Dead:
			next, acts := toBuild(p, m, a, r)
			return next, acts, true
		}
	case Diagnosing:
		switch ev := ev.(type) {
		case Diagnosed:
			m.Fixed = withFix(m.Fixed, ev.Class)
			next, acts := toBuild(p, m, a, r)
			return next, acts, true
		case Dead:
			return m, nil, true
		}
	}
	return m, nil, false
}

// answered applies the node's aggregated answer to the attempt just sent.
func answered(p Policy, m Machine, o Outcome, a population.Actor, r *rand.Rand) (Machine, []Action) {
	switch o.Verdict {
	case Accepted:
		m.State, m.Sent = Pending, true
		return m, nil
	case Unreached:
		m.Sent = true
		return toSend(p, m, a, r)
	}
	m.State = Diagnosing
	var acts []Action
	if o.Refutes(m.Sent) {
		acts = append(acts, Refused{Tx: m.Current})
	}
	return m, append(acts, Diagnose{Failures: o.Failures})
}

// toBuild asks for the next attempt, or gives up when patience is spent.
func toBuild(p Policy, m Machine, a population.Actor, r *rand.Rand) (Machine, []Action) {
	if int(m.Attempts) >= Patience(a) {
		return giveUp(m)
	}
	m.State = Build
	return m, wait(p, m, r, BuildAttempt{})
}

// toSend sends Current, counting the send, or gives up when patience is
// spent. A resend after a rollback counts like any other send.
func toSend(p Policy, m Machine, a population.Actor, r *rand.Rand) (Machine, []Action) {
	if int(m.Attempts) >= Patience(a) {
		return giveUp(m)
	}
	acts := wait(p, m, r, SendTx{Tx: m.Current})
	m.State = Submit
	m.Attempts++
	return m, acts
}

func giveUp(m Machine) (Machine, []Action) {
	m.State = GaveUp
	return m, []Action{GiveUp{}, Frustrate{}}
}

// wait puts a Backoff before act once anything has been sent.
func wait(p Policy, m Machine, r *rand.Rand, act Action) []Action {
	if m.Attempts == 0 {
		return []Action{act}
	}
	return []Action{Backoff{D: p.Backoff(int(m.Attempts), r)}, act}
}

func withFix(fixed []Class, c Class) []Class {
	if slices.Contains(fixed, c) {
		return fixed
	}
	out := append(slices.Clone(fixed), c)
	slices.Sort(out)
	return out
}

// Restart rebuilds a machine after a restart from what is on disk: m is the
// persisted machine and newest the newest unfinished attempt of the intent,
// nil if none. Every unfinished attempt counts as sent, because the process
// that died may have sent it, so a later "inputs spent" proves nothing. The
// newest is sent again as the same bytes; with none, the machine builds.
// Done and GaveUp stay. Attempts and Fixed are kept as persisted.
func Restart(p Policy, m Machine, newest *chain.TxID, a population.Actor, r *rand.Rand) (Machine, []Action, error) {
	if m.State == Done || m.State == GaveUp {
		return m, nil, nil
	}
	if newest == nil {
		m.State = Build
		next, acts := toBuild(p, m, a, r)
		return next, acts, nil
	}
	m.Current, m.Sent = *newest, true
	next, acts := toSend(p, m, a, r)
	return next, acts, nil
}
