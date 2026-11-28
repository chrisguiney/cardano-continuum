// Package send performs the engine's decided submissions against a node and
// maps each answer, as internal/submit classifies it, to the SubmitResult
// the engine logs.
package send

import (
	"context"
	"fmt"
	"slices"

	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/wire"
	"github.com/chrisguiney/cardano-continuum/internal/oracle"
	"github.com/chrisguiney/cardano-continuum/internal/submit"
)

// Node is the node as a sender uses it.
type Node interface {
	Submit(ctx context.Context, tx []byte) error
	HasTx(id wire.TxID) bool
}

// Syncer flushes the event log.
type Syncer interface {
	Sync(ctx context.Context) error
}

// Sender sends submissions to one node.
type Sender struct {
	node Node
	log  Syncer
	// who is the node that answers, for the oracle's findings; nil when the
	// node cannot say.
	who *oracle.Node
	// resumed are the intents in flight when this process started.
	resumed    map[uint64]bool
	rejections uint64
	unreached  uint64
}

// New returns a Sender to n that syncs l before each first send. resumed
// are the intents in flight at a restart: the process that died may have
// sent them, so a rejection of one proves nothing.
func New(n Node, l Syncer, who *oracle.Node, resumed []wire.Submission) *Sender {
	s := &Sender{node: n, log: l, who: who, resumed: map[uint64]bool{}}
	for _, r := range resumed {
		s.resumed[r.Intent] = true
	}
	return s
}

// Rejections counts the rejected sends so far.
func (s *Sender) Rejections() uint64 { return s.rejections }

// Unreached counts the sends whose answer is unknown so far.
func (s *Sender) Unreached() uint64 { return s.unreached }

// Send submits sub unless the mempool already holds it, then its rivals,
// then, for a DoubleSubmit, the same bytes again.
func (s *Sender) Send(ctx context.Context, sub wire.Submission) (wire.SubmitResult, error) {
	res, err := s.once(ctx, sub)
	if err != nil {
		return res, err
	}
	for _, rv := range sub.Rivals {
		res.Rivals = append(res.Rivals, s.rival(ctx, rv))
	}
	if ctx.Err() != nil {
		return res, ctx.Err()
	}
	if sub.Twice {
		// The second send goes out whatever the first answered, as an actor
		// pressing send twice would; HasTx would only hide its answer.
		o := submit.Send(ctx, target{s.node}, 0, sub.Tx)
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		res.Again = &wire.Answer{Verdict: verdictOf(o), Failures: classesOf(o), Details: detailsOf(o)}
	}
	delete(s.resumed, sub.Intent)
	return res, nil
}

func (s *Sender) once(ctx context.Context, sub wire.Submission) (wire.SubmitResult, error) {
	res := wire.SubmitResult{Intent: sub.Intent, Verdict: wire.VerdictAccepted, Node: s.who}
	if s.node.HasTx(sub.ID) {
		return res, nil
	}
	if err := s.log.Sync(ctx); err != nil {
		return res, fmt.Errorf("engine: sync before submit: %w", err)
	}
	o := submit.Send(ctx, target{s.node}, 0, sub.Tx)
	if ctx.Err() != nil {
		return res, ctx.Err()
	}
	switch o.Verdict {
	case submit.Rejected:
		res.Verdict, res.Reason = wire.VerdictRejected, (&submit.RejectedError{Failures: o.Failures}).Error()
		res.Failures, res.Details = classesOf(o), detailsOf(o)
		res.Resend = s.resumed[sub.Intent]
		s.rejections++
	case submit.Unreached:
		res.Verdict, res.Reason = wire.VerdictUnreached, o.Err.Error()
		s.unreached++
	}
	return res, nil
}

// rival sends a race's further transaction. One the mempool already holds
// was sent before a crash and is taken as accepted.
func (s *Sender) rival(ctx context.Context, rv wire.Rival) wire.Answer {
	if s.node.HasTx(rv.ID) {
		return wire.Answer{Verdict: wire.VerdictAccepted}
	}
	o := submit.Send(ctx, target{s.node}, 0, rv.Tx)
	if o.Verdict == submit.Rejected {
		s.rejections++
	}
	return wire.Answer{Verdict: verdictOf(o), Failures: classesOf(o), Details: detailsOf(o)}
}

func verdictOf(o submit.Outcome) wire.Verdict {
	switch o.Verdict {
	case submit.Accepted:
		return wire.VerdictAccepted
	case submit.Rejected:
		return wire.VerdictRejected
	}
	return wire.VerdictUnreached
}

func classesOf(o submit.Outcome) []string {
	var out []string
	for _, f := range o.Failures {
		out = append(out, string(f.Class))
	}
	return out
}

// detailsOf is the node's text per failure, or nil when no failure has
// any, so an answer without text logs as before.
func detailsOf(o submit.Outcome) []string {
	if !slices.ContainsFunc(o.Failures, func(f submit.Failure) bool { return f.Detail != "" }) {
		return nil
	}
	out := make([]string, len(o.Failures))
	for i, f := range o.Failures {
		out[i] = f.Detail
	}
	return out
}
