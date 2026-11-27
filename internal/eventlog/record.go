package eventlog

import (
	"encoding/binary"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"hash/crc32"

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
	Slot uint64 `json:"slot"`
	Hash Hex    `json:"hash"`
}

// BlockApplied is the body of a BlockApplied input.
type BlockApplied struct {
	Point ChainPoint `json:"point"`
	// Content is what the engine reads from the block, in the engine's own
	// encoding; the log never interprets it. It has to be in the input
	// because replay has no chain to fetch the block from.
	Content jsontext.Value `json:"content,omitzero"`
}

// RolledBack is the body of a RolledBack input: the chain is now at To.
type RolledBack struct {
	To ChainPoint `json:"to"`
}

// HeaderBody is the body of the header record, which must be seq 1. It holds
// a fingerprint of the root seed, never the seed.
type HeaderBody struct {
	SeedFingerprint Hex    `json:"seed_fingerprint"`
	ProfileHash     Hex    `json:"profile_hash"`
	GenesisHash     Hex    `json:"genesis_hash"`
	BinaryVersion   string `json:"binary_version"`
}

// Frame layout: u32 length of (version byte + payload), u32 CRC-32C of those
// bytes, version byte, payload. The payload is a JSON object (frame) made by
// Encode, with the record's body embedded as JSON.
const (
	frameHead = 8
	// FormatVersion is the record version byte. Version 1 was CBOR.
	FormatVersion = 2
	// MaxRecordSize bounds one frame's body, so a corrupt length field
	// cannot drive an allocation.
	MaxRecordSize = 16 << 20
	cborVersion   = 1
)

// Frame errors. ErrTorn means the input ended inside a frame, which is what
// a crash mid-append leaves. ErrCorrupt means a complete frame failed its
// checksum or did not parse. ErrOldFormat means a sound frame of the CBOR
// format, which this build cannot read; it is not corruption, so a log
// ending in one is refused rather than truncated.
var (
	ErrTorn      = errors.New("eventlog: torn record")
	ErrCorrupt   = errors.New("eventlog: corrupt record")
	ErrOldFormat = errors.New("eventlog: log written by a CBOR build; start a new run")
)

var crcTable = crc32.MakeTable(crc32.Castagnoli)

// frame is a record's payload. Declaration order is the format.
type frame struct {
	// Seq is the record's log sequence number.
	Seq uint64 `json:"seq"`
	// At is the record's simulated time.
	At int64 `json:"at,omitzero"`
	// Class is the record's Class.
	Class uint8 `json:"class"`
	// Kind names the record's body type.
	Kind string `json:"kind"`
	// Actor is the actor the record concerns, zero for none.
	Actor uint64 `json:"actor,omitzero"`
	// Body is the record's body, embedded as JSON rather than as a string.
	Body jsontext.Value `json:"body,omitzero"`
}

// AppendFrame appends the framed encoding of r to dst. r.Body must be JSON,
// as Encode makes it.
func AppendFrame(dst []byte, r Record) ([]byte, error) {
	p, err := Encode(frame{
		Seq:   r.Seq,
		At:    int64(r.At),
		Class: uint8(r.Class),
		Kind:  r.Kind,
		Actor: uint64(r.Actor),
		Body:  r.Body,
	})
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
	switch body[0] {
	case FormatVersion:
	case cborVersion:
		return Record{}, 0, ErrOldFormat
	default:
		return Record{}, 0, fmt.Errorf("%w: unknown version %d", ErrCorrupt, body[0])
	}
	var w frame
	if err := decode(body[1:], &w, maxDepth+1); err != nil {
		return Record{}, 0, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	return Record{
		Seq:   w.Seq,
		At:    sched.Time(w.At),
		Class: Class(w.Class),
		Kind:  w.Kind,
		Actor: ActorID(w.Actor),
		Body:  []byte(w.Body),
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
