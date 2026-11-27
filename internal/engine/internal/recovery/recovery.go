// Package recovery is the I/O half of the engine-recovery contract
// (models/specs/engine-recovery, design A): it writes or checks the log
// header, rebuilds the engine from the checkpoint plus the log tail
// (`Restart`), and commits checkpoints only after the log is durable
// (`CommitLog` before `CommitCkpt`). The pure `Step` half is the window
// component.
package recovery

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/oracle"
	"github.com/chrisguiney/cardano-continuum/internal/store"
)

// Crash stages: the points at which a crash hook may stop the loop.
const (
	StageFed          = "fed"          // an input and its decisions are appended
	StageSubmitted    = "submitted"    // a transaction reached the node, its result is not logged
	StageSynced       = "synced"       // the log is flushed before a checkpoint
	StageCheckpointed = "checkpointed" // a checkpoint is committed
)

// ErrDiverged: replaying the log on restart produced different decisions,
// so the log was written by a different engine or the engine is not
// deterministic.
var ErrDiverged = errors.New("engine: replay diverged from the log")

// Log is the event log as recovery uses it.
type Log interface {
	eventlog.Writer
	eventlog.Reader
	LastSeq() uint64
}

// Store is the checkpoint store as recovery uses it.
type Store interface {
	SaveCheckpoint(ctx context.Context, c store.Checkpoint) error
	Recover(ctx context.Context) (store.Recovery, error)
}

// Open reads the checkpoint, when s is non-nil, and writes the header to
// an empty log or checks the header of a non-empty one. It returns the
// checkpoint's snapshot and the log seq it covers; zero means a replay of
// the whole log.
func Open(ctx context.Context, l Log, s Store, h eventlog.HeaderBody) ([]byte, uint64, error) {
	var snap []byte
	var covers uint64
	if s != nil {
		rec, err := s.Recover(ctx)
		if err != nil {
			return nil, 0, fmt.Errorf("engine: recover: %w", err)
		}
		if rec.Checkpoint.Seq > 0 {
			snap, covers = rec.Checkpoint.Actors, rec.Checkpoint.Seq
		}
	}
	if l.LastSeq() == 0 {
		return snap, covers, writeHeader(ctx, l, h)
	}
	return snap, covers, checkHeader(ctx, l, h)
}

func writeHeader(ctx context.Context, l eventlog.Writer, h eventlog.HeaderBody) error {
	body, err := eventlog.Encode(h)
	if err != nil {
		return err
	}
	if err := l.Append(ctx, eventlog.Record{Class: eventlog.Header, Kind: eventlog.KindHeader, Body: body}); err != nil {
		return fmt.Errorf("engine: write header: %w", err)
	}
	return l.Sync(ctx)
}

func checkHeader(ctx context.Context, l eventlog.Reader, want eventlog.HeaderBody) error {
	var got []byte
	err := l.Scan(ctx, 1, func(rec eventlog.Record) error {
		if rec.Class == eventlog.Header {
			got = rec.Body
		}
		return eventlog.ErrStop
	})
	if err != nil {
		return err
	}
	w, err := eventlog.Encode(want)
	if err != nil {
		return err
	}
	if !bytes.Equal(got, w) {
		return fmt.Errorf("%w: log was recorded under another seed, profile set, genesis or binary", eventlog.ErrHeaderMismatch)
	}
	return nil
}

// Replay rebuilds the engine with f from snap and the log tail after
// covers. Decisions of the last input may be missing (they are appended
// unsynced and a crash can drop them); those are regenerated and appended.
// Any other difference is divergence.
func Replay(ctx context.Context, l Log, snap []byte, covers uint64, f eventlog.Factory) error {
	rp := eventlog.Replayer{Log: l, New: f}
	res, err := rp.Replay(ctx, snap, covers)
	if err != nil {
		return fmt.Errorf("engine: replay tail: %w", err)
	}
	if len(res.Diff) == 0 {
		return nil
	}
	var lastInput uint64
	err = l.Scan(ctx, covers+1, func(rec eventlog.Record) error {
		if rec.Class == eventlog.Input {
			lastInput = rec.Seq
		}
		return nil
	})
	if err != nil {
		return err
	}
	var missing []eventlog.Record
	for _, m := range res.Diff {
		if m.Want != nil || m.Seq != lastInput {
			return fmt.Errorf("%w: %s", ErrDiverged, m)
		}
		missing = append(missing, *m.Got)
	}
	if err := l.Append(ctx, missing...); err != nil {
		return err
	}
	return l.Sync(ctx)
}

// LoadFindings adds the findings logged up to seq covers to set. A
// checkpoint's findings are in the log, not the snapshot.
func LoadFindings(ctx context.Context, l eventlog.Reader, covers uint64, set *oracle.Set) error {
	if covers == 0 {
		return nil
	}
	err := l.Scan(ctx, 1, func(rec eventlog.Record) error {
		if rec.Seq > covers {
			return eventlog.ErrStop
		}
		return set.Load(rec)
	})
	if err != nil {
		return fmt.Errorf("engine: load findings: %w", err)
	}
	return nil
}
