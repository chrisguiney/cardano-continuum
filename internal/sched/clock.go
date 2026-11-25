package sched

import (
	"context"
	"time"
)

// Clock paces a run. Now reports simulated time; Wait blocks until simulated
// time reaches until or ctx ends, returning ctx.Err() in that case.
type Clock interface {
	Now() Time
	Wait(ctx context.Context, until Time) error
}

// Virtual is a clock that jumps: Wait advances Now to the target without
// delay. It never moves backwards. It is for --fake runs and tests, and is
// not safe for concurrent use.
type Virtual struct{ now Time }

// NewVirtual returns a Virtual clock starting at start.
func NewVirtual(start Time) *Virtual { return &Virtual{now: start} }

// Now implements Clock.
func (v *Virtual) Now() Time { return v.now }

// Wait implements Clock.
func (v *Virtual) Wait(ctx context.Context, until Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	v.now = max(v.now, until)
	return nil
}

// Wall is anchored to the network's system start: Now is the real time
// elapsed since then in milliseconds. It is the only code in this package that
// reads the wall clock. When the run falls behind, Wait returns at once for
// targets already past, so events are delayed but never reordered.
type Wall struct{ start time.Time }

// NewWall returns a clock whose Time zero is systemStart.
func NewWall(systemStart time.Time) *Wall { return &Wall{start: systemStart} }

// Now implements Clock.
func (w *Wall) Now() Time { return Time(time.Since(w.start).Milliseconds()) }

// Wait implements Clock.
func (w *Wall) Wait(ctx context.Context, until Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	d := time.Duration(until-w.Now()) * time.Millisecond
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
