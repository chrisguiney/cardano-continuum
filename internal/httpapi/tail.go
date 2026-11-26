package httpapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
)

// pollEvery is how often Run looks for appended records.
const pollEvery = 250 * time.Millisecond

// Tail follows an event log file read-only, folding each new record into a
// Projection and publishing engine decisions to a Hub. It opens the file
// read-only and never repairs it: eventlog.Open would truncate the tail a
// live writer is still appending, so a frame that is incomplete (or the
// final frame with a bad checksum) is waited out, not cut.
type Tail struct {
	f    *os.File
	proj *Projection
	hub  *Hub
	off  int64
	seq  uint64
}

// NewTail opens the log at path for reading.
func NewTail(path string, p *Projection, h *Hub) (*Tail, error) {
	f, err := os.Open(path) //nolint:gosec // operator-supplied path
	if err != nil {
		return nil, fmt.Errorf("open event log: %w", err)
	}
	return &Tail{f: f, proj: p, hub: h}, nil
}

// Close closes the file.
func (t *Tail) Close() error { return t.f.Close() }

// Poll reads every complete record appended since the last call and returns
// how many it applied. A seq gap or a corrupt frame with data after it is an
// error and leaves the offset where it was.
func (t *Tail) Poll(ctx context.Context) (int, error) {
	st, err := t.f.Stat()
	if err != nil {
		return 0, fmt.Errorf("stat event log: %w", err)
	}
	if st.Size() < t.off {
		return 0, fmt.Errorf("event log shrank from %d to %d bytes", t.off, st.Size())
	}
	n := 0
	head := make([]byte, 8)
	for {
		if err := ctx.Err(); err != nil {
			return n, err
		}
		rec, size, err := t.next(head, st.Size())
		if err != nil || size == 0 {
			return n, err
		}
		if rec.Seq != t.seq+1 {
			return n, fmt.Errorf("%w: got %d after %d", eventlog.ErrSeqGap, rec.Seq, t.seq)
		}
		t.off += size
		t.seq = rec.Seq
		t.apply(rec)
		n++
	}
}

// next reads the frame at the offset. A zero size means no complete frame is
// there yet.
func (t *Tail) next(head []byte, end int64) (eventlog.Record, int64, error) {
	if end-t.off < int64(len(head)) {
		return eventlog.Record{}, 0, nil
	}
	if _, err := t.f.ReadAt(head, t.off); err != nil {
		return eventlog.Record{}, 0, fmt.Errorf("read frame header: %w", err)
	}
	n, err := eventlog.FrameBodyLen(head)
	if err != nil {
		return eventlog.Record{}, 0, fmt.Errorf("offset %d: %w", t.off, err)
	}
	size := int64(len(head) + n)
	if t.off+size > end {
		return eventlog.Record{}, 0, nil
	}
	buf := make([]byte, size)
	if _, err := t.f.ReadAt(buf, t.off); err != nil && !errors.Is(err, io.EOF) {
		return eventlog.Record{}, 0, fmt.Errorf("read frame: %w", err)
	}
	rec, _, err := eventlog.DecodeFrame(buf)
	if errors.Is(err, eventlog.ErrCorrupt) && t.off+size == end {
		return eventlog.Record{}, 0, nil
	}
	if err != nil {
		return eventlog.Record{}, 0, fmt.Errorf("offset %d: %w", t.off, err)
	}
	return rec, size, nil
}

func (t *Tail) apply(rec eventlog.Record) { NewFeed(t.proj, t.hub).Apply(rec) }

// Feed applies records to a projection and publishes the decisions among
// them to a hub, as a Tail does for a log file. A live engine feeds every
// record it appends, in seq order, so its own Source needs no file.
type Feed struct {
	proj *Projection
	hub  *Hub
}

// NewFeed returns a feed into p and h.
func NewFeed(p *Projection, h *Hub) *Feed { return &Feed{proj: p, hub: h} }

// Apply applies one record, which must carry its seq.
func (f *Feed) Apply(rec eventlog.Record) {
	f.proj.Apply(rec)
	if rec.Class != eventlog.Decision {
		return
	}
	d, err := toDecision(rec, f.proj.Profile(rec.Actor))
	if err != nil {
		f.proj.NoteBodyError()
	}
	f.hub.Publish(d)
}

// Run polls until ctx is cancelled and returns the context's error, or the
// first Poll failure.
func (t *Tail) Run(ctx context.Context) error {
	tick := time.NewTicker(pollEvery)
	defer tick.Stop()
	for {
		if _, err := t.Poll(ctx); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}
