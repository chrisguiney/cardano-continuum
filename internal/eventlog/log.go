package eventlog

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// Writer appends records. The log assigns Seq (the next number after the last
// entry), ignoring the field on the way in. Append makes records visible to
// Scan but not durable; Sync makes everything appended so far durable, so a
// caller batches appends and syncs once.
type Writer interface {
	Append(ctx context.Context, recs ...Record) error
	Sync(ctx context.Context) error
}

// Reader iterates the log.
type Reader interface {
	// Scan calls fn for each record with Seq >= from, in order, and stops at
	// the first error fn returns. Returning ErrStop ends the scan without
	// error.
	Scan(ctx context.Context, from uint64, fn func(Record) error) error
}

// ErrStop is returned by a Scan callback to end the scan early.
var ErrStop = errors.New("eventlog: stop scan")

// ErrSeqGap reports a log whose sequence numbers skip or go backwards.
var ErrSeqGap = errors.New("eventlog: sequence gap or regression")

// seqCheck verifies that seqs run gaplessly upward from want.
type seqCheck struct{ want uint64 }

func (c *seqCheck) next(seq uint64) error {
	if c.want != 0 && seq != c.want {
		return fmt.Errorf("%w: got %d, want %d", ErrSeqGap, seq, c.want)
	}
	c.want = seq + 1
	return nil
}

// MemLog is an in-memory log for tests and the fake chain. It has no
// durability, so Sync only advances DurableSeq.
type MemLog struct {
	mu      sync.Mutex // protects recs, durable
	recs    []Record
	durable uint64
}

// Append implements Writer.
func (m *MemLog) Append(_ context.Context, recs ...Record) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range recs {
		r.Seq = uint64(len(m.recs)) + 1
		r.Body = append([]byte(nil), r.Body...)
		m.recs = append(m.recs, r)
	}
	return nil
}

// Sync implements Writer.
func (m *MemLog) Sync(context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.durable = uint64(len(m.recs))
	return nil
}

// LastSeq returns the seq of the last entry, or 0 if empty.
func (m *MemLog) LastSeq() uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return uint64(len(m.recs))
}

// DurableSeq returns the seq of the last synced entry.
func (m *MemLog) DurableSeq() uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.durable
}

// Scan implements Reader.
func (m *MemLog) Scan(ctx context.Context, from uint64, fn func(Record) error) error {
	m.mu.Lock()
	recs := append([]Record(nil), m.recs...)
	m.mu.Unlock()
	return scanSlice(ctx, recs, from, fn)
}

// TruncateAfter drops every entry with Seq > seq. A checkpoint cannot cover
// dropped entries, so the caller must not truncate below its checkpoint seq.
func (m *MemLog) TruncateAfter(_ context.Context, seq uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if seq < uint64(len(m.recs)) {
		m.recs = m.recs[:seq]
	}
	m.durable = min(m.durable, uint64(len(m.recs)))
	return nil
}

func scanSlice(ctx context.Context, recs []Record, from uint64, fn func(Record) error) error {
	var sc seqCheck
	for _, r := range recs {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := sc.next(r.Seq); err != nil {
			return err
		}
		if r.Seq < from {
			continue
		}
		if err := fn(r); err != nil {
			if errors.Is(err, ErrStop) {
				return nil
			}
			return err
		}
	}
	return nil
}
