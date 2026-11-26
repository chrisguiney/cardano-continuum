package sink

import (
	"errors"
	"fmt"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"

	"github.com/chrisguiney/cardano-continuum/internal/agenda"
	"github.com/chrisguiney/cardano-continuum/internal/engine"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/oracle"
)

// table accumulates rows for one Parquet table as an Arrow record builder.
type table struct {
	name string
	b    *array.RecordBuilder
	rows int64
}

func newTable(name string, mem memory.Allocator) *table {
	return &table{name: name, b: array.NewRecordBuilder(mem, schemas[name])}
}

func (t *table) u64(c int, v uint64) { t.b.Field(c).(*array.Uint64Builder).Append(v) }
func (t *table) u32(c int, v uint32) { t.b.Field(c).(*array.Uint32Builder).Append(v) }
func (t *table) i64(c int, v int64)  { t.b.Field(c).(*array.Int64Builder).Append(v) }
func (t *table) f64(c int, v float64) {
	t.b.Field(c).(*array.Float64Builder).Append(v)
}
func (t *table) str(c int, v string)  { t.b.Field(c).(*array.StringBuilder).Append(v) }
func (t *table) bin(c int, v []byte)  { t.b.Field(c).(*array.BinaryBuilder).Append(v) }
func (t *table) flag(c int, v bool)   { t.b.Field(c).(*array.BooleanBuilder).Append(v) }
func (t *table) hash(c int, v []byte) { t.b.Field(c).(*array.FixedSizeBinaryBuilder).Append(v) }
func (t *table) null(c int)           { t.b.Field(c).AppendNull() }

// done closes a row; every column must have been appended once.
func (t *table) done() { t.rows++ }

// take returns the buffered rows as a record and resets the builder.
func (t *table) take() arrow.RecordBatch { return t.b.NewRecordBatch() }

func (t *table) release() { t.b.Release() }

// builder turns log records into table rows. It tracks the earlier Submitted
// of each intent so a SubmitResult, which names only the intent, can be
// tied to a transaction hash and attempt number. Records at or below
// `after` update that state but emit no rows: an incremental export has to
// replay the whole log to know which attempt a late result belongs to.
type builder struct {
	after  uint64
	tables map[string]*table
	last   map[uint64]attempt
	header *eventlog.HeaderBody
}

type attempt struct {
	n  uint32
	tx engine.TxID
}

func newBuilder(after uint64, mem memory.Allocator) *builder {
	b := &builder{after: after, tables: map[string]*table{}, last: map[uint64]attempt{}}
	for _, n := range TableNames {
		b.tables[n] = newTable(n, mem)
	}
	return b
}

func (b *builder) release() {
	for _, t := range b.tables {
		t.release()
	}
}

// add processes one record.
func (b *builder) add(r eventlog.Record) error {
	if err := b.addKind(r); err != nil {
		return fmt.Errorf("seq %d kind %s: %w", r.Seq, r.Kind, err)
	}
	return nil
}

func (b *builder) addKind(r eventlog.Record) error {
	if r.Class == eventlog.Header {
		return b.addHeader(r)
	}
	emit := r.Seq > b.after
	var intent *uint64
	var kind string
	var chosen *float64
	switch r.Kind {
	case engine.KindIntentChosen:
		var d agenda.Decision
		if err := eventlog.Decode(r.Body, &d); err != nil {
			return err
		}
		kind = string(d.Intent.Kind)
		chosen = chosenScore(d)
		if emit {
			b.scores(r.Seq, d)
		}
	case engine.KindSubmitted:
		var s engine.Submitted
		if err := eventlog.Decode(r.Body, &s); err != nil {
			return err
		}
		intent = &s.Intent
		b.submitted(r, s, emit)
	case eventlog.KindSubmitResult:
		var s engine.SubmitResult
		if err := eventlog.Decode(r.Body, &s); err != nil {
			return err
		}
		intent = &s.Intent
		if emit {
			b.result(r, s)
		}
	case engine.KindConfirmed:
		var c engine.Confirmed
		if err := eventlog.Decode(r.Body, &c); err != nil {
			return err
		}
		intent = &c.Intent
		if emit {
			b.confirmed(r, c)
		}
	case engine.KindRefused, engine.KindReleased, engine.KindGaveUp:
		id, err := intentOf(r)
		if err != nil {
			return err
		}
		intent = &id
	case engine.KindSpawned, engine.KindTransition:
		if err := b.transition(r, emit); err != nil {
			return err
		}
	case eventlog.KindBlockApplied, eventlog.KindRolledBack:
		if err := b.point(r, emit); err != nil {
			return err
		}
	case oracle.KindFinding:
		if emit {
			if err := b.finding(r); err != nil {
				return err
			}
		}
	}
	if emit {
		b.decision(r, intent, kind, chosen)
	}
	return nil
}

func chosenScore(d agenda.Decision) *float64 {
	for _, s := range d.Scores {
		if s.Kind == d.Intent.Kind && s.Target == d.Intent.Target {
			v := s.Value
			return &v
		}
	}
	return nil
}

// intentOf reads the intent id of the decision kinds that carry only an
// intent and a reason.
func intentOf(r eventlog.Record) (uint64, error) {
	switch r.Kind {
	case engine.KindRefused:
		var v engine.Refused
		err := eventlog.Decode(r.Body, &v)
		return v.Intent, err
	case engine.KindReleased:
		var v engine.Released
		err := eventlog.Decode(r.Body, &v)
		return v.Intent, err
	default:
		var v engine.GaveUp
		err := eventlog.Decode(r.Body, &v)
		return v.Intent, err
	}
}

func (b *builder) decision(r eventlog.Record, intent *uint64, kind string, chosen *float64) {
	t := b.tables[tDecision]
	t.u64(0, r.Seq)
	t.i64(1, int64(r.At))
	t.str(2, className(r.Class))
	t.u64(3, uint64(r.Actor))
	t.str(4, r.Kind)
	if intent != nil {
		t.u64(5, *intent)
	} else {
		t.null(5)
	}
	if kind != "" {
		t.str(6, kind)
	} else {
		t.null(6)
	}
	if chosen != nil {
		t.f64(7, *chosen)
	} else {
		t.null(7)
	}
	t.done()
}

func className(c eventlog.Class) string {
	switch c {
	case eventlog.Input:
		return "Input"
	case eventlog.Decision:
		return "Decision"
	case eventlog.Header:
		return "Header"
	}
	return fmt.Sprintf("Class%d", uint8(c))
}

func (b *builder) scores(seq uint64, d agenda.Decision) {
	t := b.tables[tScore]
	for i, s := range d.Scores {
		t.u64(0, seq)
		t.u32(1, uint32(i))
		t.str(2, string(s.Kind))
		if s.Target != 0 {
			t.u64(3, uint64(s.Target))
		} else {
			t.null(3)
		}
		t.f64(4, s.Value)
		t.str(5, s.Why)
		t.done()
	}
}

func (b *builder) submitted(r eventlog.Record, s engine.Submitted, emit bool) {
	a := b.last[s.Intent]
	a = attempt{n: a.n + 1, tx: s.ID}
	b.last[s.Intent] = a
	if !emit {
		return
	}
	t := b.tables[tSubmission]
	t.u64(0, r.Seq)
	t.i64(1, int64(r.At))
	t.u64(2, uint64(r.Actor))
	t.u64(3, s.Intent)
	t.u32(4, a.n)
	t.hash(5, s.ID[:])
	t.u64(6, s.To)
	t.u64(7, s.Amount)
	t.u64(8, s.Fee)
	t.u64(9, s.TTL)
	t.done()
}

func verdictName(v engine.Verdict) string {
	switch v {
	case engine.VerdictAccepted:
		return "accepted"
	case engine.VerdictRejected:
		return "rejected"
	case engine.VerdictUnreached:
		return "unreached"
	}
	return "unknown"
}

func (b *builder) result(r eventlog.Record, s engine.SubmitResult) {
	t := b.tables[tResult]
	t.u64(0, r.Seq)
	t.i64(1, int64(r.At))
	t.u64(2, s.Intent)
	if a, ok := b.last[s.Intent]; ok {
		t.u32(3, a.n)
		t.hash(4, a.tx[:])
	} else {
		t.null(3)
		t.null(4)
	}
	t.str(5, verdictName(s.Verdict))
	if s.Reason != "" {
		t.str(6, s.Reason)
	} else {
		t.null(6)
	}
	t.flag(7, s.Resend)
	t.done()
	f := b.tables[tFailure]
	for i, c := range s.Failures {
		f.u64(0, r.Seq)
		f.u32(1, uint32(i))
		f.str(2, c)
		f.done()
	}
}

func (b *builder) confirmed(r eventlog.Record, c engine.Confirmed) {
	t := b.tables[tConfirm]
	t.u64(0, r.Seq)
	t.i64(1, int64(r.At))
	t.u64(2, uint64(r.Actor))
	t.u64(3, c.Intent)
	t.hash(4, c.ID[:])
	t.done()
}

func (b *builder) transition(r eventlog.Record, emit bool) error {
	var from, to, profile string
	if r.Kind == engine.KindSpawned {
		var s engine.Spawned
		if err := eventlog.Decode(r.Body, &s); err != nil {
			return err
		}
		profile = s.Profile
	} else {
		var s engine.Transition
		if err := eventlog.Decode(r.Body, &s); err != nil {
			return err
		}
		from, to = s.From, s.To
	}
	if !emit {
		return nil
	}
	t := b.tables[tTransition]
	t.u64(0, r.Seq)
	t.i64(1, int64(r.At))
	t.u64(2, uint64(r.Actor))
	for c, v := range map[int]string{3: from, 4: to, 5: profile} {
		if v == "" {
			t.null(c)
		} else {
			t.str(c, v)
		}
	}
	t.done()
	return nil
}

// errHashLen marks a chain point whose hash is not a Blake2b-256 digest.
var errHashLen = errors.New("block hash is not 32 bytes")

func (b *builder) point(r eventlog.Record, emit bool) error {
	var p eventlog.ChainPoint
	name := tBlock
	if r.Kind == eventlog.KindBlockApplied {
		var v eventlog.BlockApplied
		if err := eventlog.Decode(r.Body, &v); err != nil {
			return err
		}
		p = v.Point
	} else {
		var v eventlog.RolledBack
		if err := eventlog.Decode(r.Body, &v); err != nil {
			return err
		}
		p, name = v.To, tRollback
	}
	// A rollback to origin carries no hash; a block always has one.
	if n := len(p.Hash); n != 32 && (n != 0 || name == tBlock) {
		return fmt.Errorf("%w: %d", errHashLen, n)
	}
	if !emit {
		return nil
	}
	t := b.tables[name]
	t.u64(0, r.Seq)
	t.u64(1, p.Slot)
	if len(p.Hash) == 0 {
		t.null(2)
	} else {
		t.hash(2, p.Hash)
	}
	t.done()
	return nil
}

// addHeader records the run header. Its row is emitted once, with the first
// export; later exports only compare fingerprints.
func (b *builder) addHeader(r eventlog.Record) error {
	var h eventlog.HeaderBody
	if err := eventlog.Decode(r.Body, &h); err != nil {
		return fmt.Errorf("seq %d: header: %w", r.Seq, err)
	}
	b.header = &h
	if r.Seq <= b.after {
		return nil
	}
	t := b.tables[tRun]
	t.u32(0, eventlog.FormatVersion)
	t.u32(1, SchemaVersion)
	t.bin(2, h.SeedFingerprint)
	t.bin(3, h.ProfileHash)
	t.bin(4, h.GenesisHash)
	t.str(5, h.BinaryVersion)
	t.done()
	return nil
}
