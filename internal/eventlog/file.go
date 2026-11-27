package eventlog

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
)

// FileLog is a log in one file of framed records. Open scans it, truncates a
// torn tail and rebuilds an offset index, so Scan from a seq reads only the
// tail. It is the shell around the pure codec.
type FileLog struct {
	f *os.File

	mu      sync.Mutex // protects offs, size, durable, closed, failed
	offs    []int64    // offs[i] is the file offset of seq i+1
	size    int64
	durable uint64
	closed  bool
	// failed is set when a torn tail could not be cut: the file and the
	// index disagree, so every later write is refused.
	failed error
}

// Open opens or creates the log at path. A final frame that is incomplete,
// or complete with a bad checksum and nothing after it, is a crash mid-append:
// it is cut off. A bad frame followed by more data, or a seq gap, is
// corruption and fails Open, because truncating would silently drop
// committed entries.
func Open(path string) (*FileLog, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("eventlog: open %s: %w", path, err)
	}
	l := &FileLog{f: f}
	if err := l.recover(); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("eventlog: recover %s: %w", path, err)
	}
	return l, nil
}

func (l *FileLog) recover() error {
	st, err := l.f.Stat()
	if err != nil {
		return err
	}
	var off int64
	var sc seqCheck
	head := make([]byte, frameHead)
	for off < st.Size() {
		_, err := l.f.ReadAt(head, off)
		if err != nil {
			if errors.Is(err, io.EOF) {
				break // torn header
			}
			return err
		}
		n, err := FrameBodyLen(head)
		if err != nil {
			return fmt.Errorf("offset %d: %w", off, err)
		}
		end := off + frameHead + int64(n)
		if end > st.Size() {
			break // torn body
		}
		buf := make([]byte, frameHead+n)
		if _, err := l.f.ReadAt(buf, off); err != nil {
			return err
		}
		rec, _, err := DecodeFrame(buf)
		if err != nil {
			if end == st.Size() && errors.Is(err, ErrCorrupt) {
				break // torn final frame
			}
			return fmt.Errorf("offset %d: %w", off, err)
		}
		if err := sc.next(rec.Seq); err != nil {
			return err
		}
		if len(l.offs) == 0 && rec.Seq != 1 {
			return fmt.Errorf("%w: first seq %d", ErrSeqGap, rec.Seq)
		}
		l.offs = append(l.offs, off)
		off = end
	}
	if off < st.Size() {
		if err := l.f.Truncate(off); err != nil {
			return err
		}
		if err := l.f.Sync(); err != nil {
			return err
		}
	}
	l.size = off
	l.durable = uint64(len(l.offs))
	return nil
}

// Append implements Writer. The write reaches the OS but is not durable
// until Sync.
func (l *FileLog) Append(ctx context.Context, recs ...Record) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return os.ErrClosed
	}
	if l.failed != nil {
		return fmt.Errorf("eventlog: log failed: %w", l.failed)
	}
	var buf []byte
	offs := make([]int64, 0, len(recs))
	next := uint64(len(l.offs)) + 1
	for _, r := range recs {
		r.Seq = next
		next++
		offs = append(offs, l.size+int64(len(buf)))
		var err error
		if buf, err = AppendFrame(buf, r); err != nil {
			return err
		}
	}
	if _, err := l.f.WriteAt(buf, l.size); err != nil {
		// A partial write leaves a torn tail; cut it so the index and
		// file agree.
		werr := fmt.Errorf("eventlog: append: %w", err)
		if terr := l.f.Truncate(l.size); terr != nil {
			l.failed = fmt.Errorf("eventlog: truncate torn tail: %w", terr)
			return errors.Join(werr, l.failed)
		}
		return werr
	}
	l.size += int64(len(buf))
	l.offs = append(l.offs, offs...)
	return nil
}

// Sync implements Writer: fsync, then DurableSeq equals LastSeq.
func (l *FileLog) Sync(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return os.ErrClosed
	}
	if err := l.f.Sync(); err != nil {
		return fmt.Errorf("eventlog: sync: %w", err)
	}
	l.durable = uint64(len(l.offs))
	return nil
}

// LastSeq returns the seq of the last entry, or 0 if empty.
func (l *FileLog) LastSeq() uint64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return uint64(len(l.offs))
}

// DurableSeq returns the seq of the last fsynced entry. A checkpoint may
// cover at most this seq.
func (l *FileLog) DurableSeq() uint64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.durable
}

// Scan implements Reader.
func (l *FileLog) Scan(ctx context.Context, from uint64, fn func(Record) error) error {
	l.mu.Lock()
	n := len(l.offs)
	if n == 0 || from > uint64(n) {
		l.mu.Unlock()
		return nil
	}
	from = max(from, 1)
	start := l.offs[from-1]
	size := l.size
	l.mu.Unlock()

	r := io.NewSectionReader(l.f, start, size-start)
	head := make([]byte, frameHead)
	seq := from
	for seq <= uint64(n) {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := io.ReadFull(r, head); err != nil {
			return fmt.Errorf("eventlog: scan seq %d: %w", seq, err)
		}
		bl, err := FrameBodyLen(head)
		if err != nil {
			return err
		}
		buf := make([]byte, frameHead+bl)
		copy(buf, head)
		if _, err := io.ReadFull(r, buf[frameHead:]); err != nil {
			return fmt.Errorf("eventlog: scan seq %d: %w", seq, err)
		}
		rec, _, err := DecodeFrame(buf)
		if err != nil {
			return fmt.Errorf("eventlog: scan seq %d: %w", seq, err)
		}
		if rec.Seq != seq {
			return fmt.Errorf("%w: got %d, want %d", ErrSeqGap, rec.Seq, seq)
		}
		if err := fn(rec); err != nil {
			if errors.Is(err, ErrStop) {
				return nil
			}
			return err
		}
		seq++
	}
	return nil
}

// TruncateAfter durably drops every entry with Seq > seq. It is how a store
// discards a log suffix that was never covered by a checkpoint; truncating
// below a checkpoint's seq would orphan that checkpoint, so callers refuse
// it. Seqs are then reused by later appends.
func (l *FileLog) TruncateAfter(ctx context.Context, seq uint64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return os.ErrClosed
	}
	if seq >= uint64(len(l.offs)) {
		return nil
	}
	cut := l.offs[seq]
	if err := l.f.Truncate(cut); err != nil {
		return fmt.Errorf("eventlog: truncate: %w", err)
	}
	if err := l.f.Sync(); err != nil {
		return fmt.Errorf("eventlog: truncate sync: %w", err)
	}
	l.offs = l.offs[:seq]
	l.size = cut
	l.durable = min(l.durable, seq)
	return nil
}

// Close syncs and closes the file.
func (l *FileLog) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	l.closed = true
	err := l.f.Sync()
	if cerr := l.f.Close(); err == nil {
		err = cerr
	}
	return err
}
