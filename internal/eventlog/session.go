package eventlog

import (
	"context"
	"errors"
	"fmt"

	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

// Session is the live path: it logs each input before the engine acts on it.
// The input is appended and synced, and only then handled, so no effect or
// checkpoint can reflect an input a crash would lose. Decisions are appended
// afterwards without a sync of their own: replay regenerates them, so losing
// them costs a comparison, never state.
type Session struct {
	Eng Engine
	Log Writer
}

// Feed logs in (Seq is assigned), syncs, hands it to the engine and logs the
// decisions it returns. When the log reports its last seq, the engine sees
// the input with the seq it was logged at, as replay hands it over, so a
// decision may name its input's seq.
func (s *Session) Feed(ctx context.Context, in Record) error {
	in.Class = Input
	if err := s.Log.Append(ctx, in); err != nil {
		return err
	}
	if l, ok := s.Log.(interface{ LastSeq() uint64 }); ok {
		in.Seq = l.LastSeq()
	}
	if err := s.Log.Sync(ctx); err != nil {
		return err
	}
	out, err := s.Eng.Handle(ctx, in)
	if err != nil {
		return fmt.Errorf("eventlog: handle %s: %w", in.Kind, err)
	}
	for i := range out {
		out[i].Class = Decision
	}
	return s.Log.Append(ctx, out...)
}

// Tick records a clock read that drives a decision. Any wall-clock or timer
// value the engine acts on must come through here, never from time.Now.
func (s *Session) Tick(ctx context.Context, now sched.Time) error {
	return s.Feed(ctx, Record{At: now, Kind: KindTick})
}

// ErrIntersectAhead reports a FindIntersect answer above the stored tip,
// which a node that only rolls back cannot produce.
var ErrIntersectAhead = errors.New("eventlog: intersection above stored tip")

// Intersect handles the answer to the restart's FindIntersect. An answer
// below the stored tip is a rollback that happened while the engine was down:
// it is logged as a RolledBack input before any new block, then handled like
// any other rollback. It reports whether a record was written.
func (s *Session) Intersect(ctx context.Context, at sched.Time, tip, answer ChainPoint) (bool, error) {
	if answer.Slot > tip.Slot {
		return false, fmt.Errorf("%w: %d > %d", ErrIntersectAhead, answer.Slot, tip.Slot)
	}
	if answer.Slot == tip.Slot && string(answer.Hash) == string(tip.Hash) {
		return false, nil
	}
	body, err := Encode(RolledBack{To: answer})
	if err != nil {
		return false, err
	}
	return true, s.Feed(ctx, Record{At: at, Kind: KindRolledBack, Body: body})
}
