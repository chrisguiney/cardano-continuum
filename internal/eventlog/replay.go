package eventlog

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// Engine is what replay and Session drive: one pure step per input. The live
// loop and replay call the same Handle, so equal inputs give equal decisions.
type Engine interface {
	// Handle applies one input and returns the decisions it caused, in order.
	Handle(ctx context.Context, in Record) ([]Record, error)
	// Snapshot returns the engine's state in a canonical encoding.
	Snapshot() ([]byte, error)
}

// Factory builds an engine from a snapshot, or a fresh one from nil.
type Factory func(snapshot []byte) (Engine, error)

// Mismatch is one place where replay disagreed with the log.
type Mismatch struct {
	// Seq is the seq of the logged decision that differs, or of the input
	// whose decisions were missing or extra.
	Seq  uint64
	Want *Record // logged; nil when replay produced an extra decision
	Got  *Record // replayed; nil when the log has a decision replay lacks
}

func (m Mismatch) String() string {
	switch {
	case m.Want == nil:
		return fmt.Sprintf("seq %d: replay produced extra decision %s", m.Seq, m.Got.Kind)
	case m.Got == nil:
		return fmt.Sprintf("seq %d: logged decision %s not reproduced", m.Seq, m.Want.Kind)
	}
	return fmt.Sprintf("seq %d: logged %s, replayed %s", m.Seq, m.Want.Kind, m.Got.Kind)
}

// Diff lists mismatches in log order. Empty means replay reproduced the log.
type Diff []Mismatch

// ErrHeaderMismatch reports a log recorded under a different seed
// fingerprint, profile set, genesis or binary version.
var ErrHeaderMismatch = errors.New("eventlog: header mismatch")

// Result is the outcome of a replay.
type Result struct {
	Diff  Diff
	State []byte // engine snapshot after the last record
	Last  uint64 // seq of the last record read, or the start seq if none
}

// Replayer feeds a log's Inputs to a fresh engine and compares the engine's
// decisions with the logged Decisions.
type Replayer struct {
	New    Factory
	Log    Reader
	Header *HeaderBody // when non-nil, the log's header must equal it
}

// Run replays the whole log.
func (r *Replayer) Run(ctx context.Context) (Diff, error) {
	res, err := r.Replay(ctx, nil, 0)
	return res.Diff, err
}

// Replay builds an engine from snapshot (nil for fresh) and replays the
// records after seq covers, which is how recovery replays the tail after a
// checkpoint. The header is checked before any record is fed, and only when
// replaying from the start.
func (r *Replayer) Replay(ctx context.Context, snapshot []byte, covers uint64) (Result, error) {
	res := Result{Last: covers}
	if r.Header != nil && covers == 0 {
		if err := r.checkHeader(ctx); err != nil {
			return res, err
		}
	}
	eng, err := r.New(snapshot)
	if err != nil {
		return res, fmt.Errorf("eventlog: build engine: %w", err)
	}

	var (
		pending  []Record // replayed decisions not yet matched
		inputSeq uint64
		seenIn   bool
	)
	flush := func() {
		for i := range pending {
			g := pending[i]
			res.Diff = append(res.Diff, Mismatch{Seq: inputSeq, Got: &g})
		}
		pending = nil
	}
	err = r.Log.Scan(ctx, covers+1, func(rec Record) error {
		res.Last = rec.Seq
		switch rec.Class {
		case Header:
			return nil
		case Input:
			flush()
			seenIn, inputSeq = true, rec.Seq
			out, err := eng.Handle(ctx, rec)
			if err != nil {
				return fmt.Errorf("eventlog: seq %d %s: %w", rec.Seq, rec.Kind, err)
			}
			pending = out
		case Decision:
			if !seenIn {
				return nil // belongs to an input the checkpoint covers
			}
			w := rec
			if len(pending) == 0 {
				res.Diff = append(res.Diff, Mismatch{Seq: rec.Seq, Want: &w})
				return nil
			}
			g := pending[0]
			pending = pending[1:]
			if !sameDecision(rec, g) {
				res.Diff = append(res.Diff, Mismatch{Seq: rec.Seq, Want: &w, Got: &g})
			}
		}
		return nil
	})
	if err != nil {
		return res, err
	}
	flush()
	if res.State, err = eng.Snapshot(); err != nil {
		return res, fmt.Errorf("eventlog: snapshot: %w", err)
	}
	return res, nil
}

func sameDecision(a, b Record) bool {
	return a.At == b.At && a.Kind == b.Kind && a.Actor == b.Actor && bytes.Equal(a.Body, b.Body)
}

func (r *Replayer) checkHeader(ctx context.Context) error {
	var got *HeaderBody
	err := r.Log.Scan(ctx, 1, func(rec Record) error {
		if rec.Class == Header {
			var h HeaderBody
			if err := Decode(rec.Body, &h); err != nil {
				return err
			}
			got = &h
		}
		return ErrStop
	})
	if err != nil {
		return err
	}
	if got == nil {
		return fmt.Errorf("%w: log has no header", ErrHeaderMismatch)
	}
	w := r.Header
	switch {
	case !bytes.Equal(got.SeedFingerprint, w.SeedFingerprint):
		return fmt.Errorf("%w: seed fingerprint", ErrHeaderMismatch)
	case !bytes.Equal(got.ProfileHash, w.ProfileHash):
		return fmt.Errorf("%w: profile hash", ErrHeaderMismatch)
	case !bytes.Equal(got.GenesisHash, w.GenesisHash):
		return fmt.Errorf("%w: genesis hash", ErrHeaderMismatch)
	case got.BinaryVersion != w.BinaryVersion:
		return fmt.Errorf("%w: binary version %q, want %q", ErrHeaderMismatch, got.BinaryVersion, w.BinaryVersion)
	}
	return nil
}

// LeaksSecret scans every record for any of secrets and returns the seq of
// the first record containing one, or 0.
func LeaksSecret(ctx context.Context, r Reader, secrets ...[]byte) (uint64, error) {
	var hit uint64
	err := r.Scan(ctx, 1, func(rec Record) error {
		for _, s := range secrets {
			if leaks(rec, s) {
				hit = rec.Seq
				return ErrStop
			}
		}
		return nil
	})
	return hit, err
}

// leaks reports whether rec holds s raw or in a form the codec writes bytes
// in: lowercase hex (Hex) or standard base64 ([]byte). The base64 form is
// matched on s's leading whole 3-byte groups, which encode the same wherever
// s ends, so s must start a byte field for that form to be found.
func leaks(rec Record, s []byte) bool {
	if len(s) == 0 {
		return false
	}
	forms := [][]byte{s, hex.AppendEncode(nil, s)}
	if n := len(s) / 3 * 3; n > 0 {
		forms = append(forms, base64.StdEncoding.AppendEncode(nil, s[:n]))
	}
	for _, f := range forms {
		if bytes.Contains(rec.Body, f) || strings.Contains(rec.Kind, string(f)) {
			return true
		}
	}
	return false
}
