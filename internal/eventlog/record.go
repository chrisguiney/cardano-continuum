// Package eventlog is the append-only record of every external input to the
// engine and every decision the engine took from them, plus the replay that
// proves the decisions follow from the inputs.
//
// The record codec, diffing and replay are pure. File and in-memory logs are
// the I/O shell. The log goes first: an input is appended and synced before
// any effect or checkpoint reflects it, so recovery is checkpoint plus a
// replay of the tail (Scan from checkpoint seq + 1).
package eventlog

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"

	"github.com/fxamacker/cbor/v2"

	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

// Class says whether a record is an external input or an engine decision.
type Class uint8

// Record classes. Replay feeds Inputs and compares Decisions.
const (
	Input    Class = 1
	Decision Class = 2
	Header   Class = 3
)

// ActorID identifies an actor in a record.
type ActorID uint64

// Record is one log entry. Seq is assigned by the log on Append, starts at 1
// and is gapless; zero means "no entry" (a checkpoint covering seq 0 covers
// nothing).
type Record struct {
	Seq   uint64
	At    sched.Time
	Class Class
	Kind  string
	Actor ActorID
	Body  []byte
}

// Input kinds. Every nondeterministic input is one of these, including the
// restart's FindIntersect answer (a RolledBack) and every clock read that
// drives a decision (a Tick).
const (
	KindHeader          = "Header"
	KindBlockApplied    = "BlockApplied"
	KindRolledBack      = "RolledBack"
	KindSubmitResult    = "SubmitResult"
	KindEpochSnapshot   = "EpochSnapshot"
	KindPoolStatus      = "PoolStatus"
	KindControl         = "Control"
	KindMonitorSnapshot = "MonitorSnapshot"
	KindTick            = "Tick"
)

// ChainPoint is a slot and block hash.
type ChainPoint struct {
	Slot uint64 `cbor:"1,keyasint"`
	Hash []byte `cbor:"2,keyasint"`
}

// BlockApplied is the body of a BlockApplied input.
type BlockApplied struct {
	Point ChainPoint `cbor:"1,keyasint"`
	// Content is what the engine reads from the block, in the engine's own
	// encoding; the log never interprets it. It has to be in the input
	// because replay has no chain to fetch the block from.
	Content []byte `cbor:"2,keyasint,omitempty"`
}

// RolledBack is the body of a RolledBack input: the chain is now at To.
type RolledBack struct {
	To ChainPoint `cbor:"1,keyasint"`
}

// HeaderBody is the body of the header record, which must be seq 1. It holds
// a fingerprint of the root seed, never the seed.
type HeaderBody struct {
	SeedFingerprint []byte `cbor:"1,keyasint"`
	ProfileHash     []byte `cbor:"2,keyasint"`
	GenesisHash     []byte `cbor:"3,keyasint"`
	BinaryVersion   string `cbor:"4,keyasint"`
}

// Frame layout: u32 length of (version byte + payload), u32 CRC-32C of those
// bytes, version byte, payload. The payload is canonical CBOR of wire.
const (
	frameHead = 8
	// FormatVersion is the record version byte.
	FormatVersion = 1
	// MaxRecordSize bounds one frame's body, so a corrupt length field
	// cannot drive an allocation.
	MaxRecordSize = 16 << 20
)

// Frame errors. ErrTorn means the input ended inside a frame, which is what
// a crash mid-append leaves. ErrCorrupt means a complete frame failed its
// checksum or did not parse.
var (
	ErrTorn    = errors.New("eventlog: torn record")
	ErrCorrupt = errors.New("eventlog: corrupt record")
)

var crcTable = crc32.MakeTable(crc32.Castagnoli)

type wire struct {
	_     struct{} `cbor:",toarray"`
	Seq   uint64
	At    int64
	Class uint8
	Kind  string
	Actor uint64
	Body  []byte
}

var (
	encMode  cbor.EncMode
	decMode  cbor.DecMode // record bodies: generous element bounds
	wireMode cbor.DecMode // the frame payload: a fixed six-element array
)

func init() {
	var err error
	if encMode, err = cbor.CanonicalEncOptions().EncMode(); err != nil {
		panic("eventlog: cbor enc mode: " + err.Error())
	}
	opts := cbor.DecOptions{
		MaxArrayElements:  1 << 20,
		MaxMapPairs:       1 << 20,
		MaxNestedLevels:   16,
		DupMapKey:         cbor.DupMapKeyEnforcedAPF,
		IndefLength:       cbor.IndefLengthForbidden,
		ExtraReturnErrors: cbor.ExtraDecErrorUnknownField,
	}
	if decMode, err = opts.DecMode(); err != nil {
		panic("eventlog: cbor dec mode: " + err.Error())
	}
	opts.MaxArrayElements, opts.MaxMapPairs = 16, 16
	if wireMode, err = opts.DecMode(); err != nil {
		panic("eventlog: cbor wire dec mode: " + err.Error())
	}
}

// Encode returns the canonical CBOR of v: map keys sorted bytewise, shortest
// integer forms, so equal values give equal bytes whatever the insertion
// order. Record bodies are made with it.
func Encode(v any) ([]byte, error) {
	b, err := encMode.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("eventlog: encode: %w", err)
	}
	return b, nil
}

// Decode parses CBOR produced by Encode into v.
func Decode(b []byte, v any) error {
	if err := decMode.Unmarshal(b, v); err != nil {
		return fmt.Errorf("eventlog: decode: %w", err)
	}
	return nil
}

// AppendFrame appends the framed encoding of r to dst.
func AppendFrame(dst []byte, r Record) ([]byte, error) {
	p, err := Encode(wire{Seq: r.Seq, At: int64(r.At), Class: uint8(r.Class), Kind: r.Kind, Actor: uint64(r.Actor), Body: r.Body})
	if err != nil {
		return dst, err
	}
	n := 1 + len(p)
	if n > MaxRecordSize {
		return dst, fmt.Errorf("eventlog: record of %d bytes exceeds %d", n, MaxRecordSize)
	}
	body := make([]byte, 0, n)
	body = append(body, FormatVersion)
	body = append(body, p...)
	dst = binary.BigEndian.AppendUint32(dst, uint32(n))
	dst = binary.BigEndian.AppendUint32(dst, crc32.Checksum(body, crcTable))
	return append(dst, body...), nil
}

// FrameBodyLen reads a frame header and returns the length of the frame body
// that follows it, or an error if the length is out of bounds. It allocates
// nothing, so a reader can check it before sizing a buffer.
func FrameBodyLen(head []byte) (int, error) {
	if len(head) < frameHead {
		return 0, ErrTorn
	}
	n := binary.BigEndian.Uint32(head)
	if n < 1 || n > MaxRecordSize {
		return 0, fmt.Errorf("%w: length %d", ErrCorrupt, n)
	}
	return int(n), nil
}

// DecodeFrame parses one frame from the front of b and returns the record
// and the bytes consumed. It never panics and allocates at most the record's
// own body.
func DecodeFrame(b []byte) (Record, int, error) {
	n, err := FrameBodyLen(b)
	if err != nil {
		return Record{}, 0, err
	}
	if len(b) < frameHead+n {
		return Record{}, 0, ErrTorn
	}
	body := b[frameHead : frameHead+n]
	if crc32.Checksum(body, crcTable) != binary.BigEndian.Uint32(b[4:]) {
		return Record{}, 0, fmt.Errorf("%w: checksum", ErrCorrupt)
	}
	if body[0] != FormatVersion {
		return Record{}, 0, fmt.Errorf("%w: unknown version %d", ErrCorrupt, body[0])
	}
	var w wire
	if err := wireMode.Unmarshal(body[1:], &w); err != nil {
		return Record{}, 0, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	return Record{
		Seq: w.Seq, At: sched.Time(w.At), Class: Class(w.Class),
		Kind: w.Kind, Actor: ActorID(w.Actor), Body: w.Body,
	}, frameHead + n, nil
}

// Retains reports whether a record must stay in the log for the engine to
// rebuild its intersection points. FindIntersect needs points down to the
// confirmed point or it falls back to origin; those points are the chain
// moves (BlockApplied, RolledBack) since the confirmed block, so the log may
// be pruned only below the seq of the first such record at or above it.
func Retains(r Record) bool {
	return r.Class == Input && (r.Kind == KindBlockApplied || r.Kind == KindRolledBack)
}

// ApplyPoint folds a chain-move record into a points list, newest last: a
// BlockApplied pushes its point, a RolledBack drops every point above To.
// Other records leave the list unchanged. It is pure, so the store can
// rebuild the list from a checkpoint's points plus the log tail.
func ApplyPoint(points []ChainPoint, r Record) ([]ChainPoint, error) {
	if r.Class != Input {
		return points, nil
	}
	switch r.Kind {
	case KindBlockApplied:
		var b BlockApplied
		if err := Decode(r.Body, &b); err != nil {
			return points, err
		}
		return append(points, b.Point), nil
	case KindRolledBack:
		var b RolledBack
		if err := Decode(r.Body, &b); err != nil {
			return points, err
		}
		for len(points) > 0 && points[len(points)-1].Slot > b.To.Slot {
			points = points[:len(points)-1]
		}
		return points, nil
	}
	return points, nil
}
