package eventlog

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

// toy is a minimal deterministic engine: a counter per actor that blocks
// raise and rollbacks undo, and a decision that records the last clock read.
type toy struct {
	S toyState
}

type toyState struct {
	Blocks   []toyBlock   `cbor:"1,keyasint"`
	Counters []toyCounter `cbor:"2,keyasint"` // sorted by actor
	Tick     int64        `cbor:"3,keyasint"`
}

type toyBlock struct {
	Slot  uint64 `cbor:"1,keyasint"`
	Actor uint64 `cbor:"2,keyasint"`
}

type toyCounter struct {
	Actor uint64 `cbor:"1,keyasint"`
	N     int    `cbor:"2,keyasint"`
}

func newToy(snap []byte) (Engine, error) {
	t := &toy{}
	if snap != nil {
		if err := Decode(snap, &t.S); err != nil {
			return nil, err
		}
	}
	return t, nil
}

func (t *toy) bump(actor uint64, d int) int {
	for i := range t.S.Counters {
		if t.S.Counters[i].Actor == actor {
			t.S.Counters[i].N += d
			return t.S.Counters[i].N
		}
	}
	t.S.Counters = append(t.S.Counters, toyCounter{actor, d})
	sort.Slice(t.S.Counters, func(i, j int) bool { return t.S.Counters[i].Actor < t.S.Counters[j].Actor })
	return d
}

func (t *toy) decision(in Record, actor uint64, n int) ([]Record, error) {
	body, err := Encode(map[string]int64{"n": int64(n), "tick": t.S.Tick})
	if err != nil {
		return nil, err
	}
	return []Record{{At: in.At, Kind: "Transition", Actor: ActorID(actor), Body: body}}, nil
}

func (t *toy) Handle(_ context.Context, in Record) ([]Record, error) {
	switch in.Kind {
	case KindTick:
		t.S.Tick = int64(in.At)
		return nil, nil
	case KindBlockApplied:
		var b BlockApplied
		if err := Decode(in.Body, &b); err != nil {
			return nil, err
		}
		t.S.Blocks = append(t.S.Blocks, toyBlock{b.Point.Slot, uint64(in.Actor)})
		return t.decision(in, uint64(in.Actor), t.bump(uint64(in.Actor), 1))
	case KindRolledBack:
		var b RolledBack
		if err := Decode(in.Body, &b); err != nil {
			return nil, err
		}
		var out []Record
		for len(t.S.Blocks) > 0 && t.S.Blocks[len(t.S.Blocks)-1].Slot > b.To.Slot {
			top := t.S.Blocks[len(t.S.Blocks)-1]
			t.S.Blocks = t.S.Blocks[:len(t.S.Blocks)-1]
			d, err := t.decision(in, top.Actor, t.bump(top.Actor, -1))
			if err != nil {
				return nil, err
			}
			out = append(out, d...)
		}
		return out, nil
	}
	return nil, nil
}

func (t *toy) Snapshot() ([]byte, error) { return Encode(t.S) }

func block(t testing.TB, slot, actor uint64) Record {
	t.Helper()
	body, err := Encode(BlockApplied{Point: ChainPoint{Slot: slot, Hash: binary.BigEndian.AppendUint64(nil, slot)}})
	if err != nil {
		t.Fatal(err)
	}
	return Record{At: sched.Time(slot), Class: Input, Kind: KindBlockApplied, Actor: ActorID(actor), Body: body}
}

func rollback(t testing.TB, to uint64) Record {
	t.Helper()
	body, err := Encode(RolledBack{To: ChainPoint{Slot: to, Hash: binary.BigEndian.AppendUint64(nil, to)}})
	if err != nil {
		t.Fatal(err)
	}
	return Record{Class: Input, Kind: KindRolledBack, Body: body}
}

func session(t testing.TB, log Writer) *Session {
	t.Helper()
	e, err := newToy(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &Session{Eng: e, Log: log}
}

// drive feeds n pseudo-random inputs: forward blocks, occasional rollbacks
// and ticks. It returns the live engine's final snapshot.
func drive(t testing.TB, s *Session, n int) []byte {
	t.Helper()
	ctx := context.Background()
	rng := rand.New(rand.NewPCG(1, 2))
	tip := uint64(0)
	for i := 0; i < n; i++ {
		var err error
		switch rng.IntN(6) {
		case 0:
			err = s.Tick(ctx, sched.Time(1000+i))
		case 1:
			if tip > 2 {
				to := tip - uint64(1+rng.IntN(2))
				err = s.Feed(ctx, rollback(t, to))
				tip = to
				break
			}
			fallthrough
		default:
			tip++
			err = s.Feed(ctx, block(t, tip, uint64(rng.IntN(3))))
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	snap, err := s.Eng.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	return snap
}

func TestEncodeCanonical(t *testing.T) {
	a := map[string]int{"a": 1, "b": 2, "c": 3}
	b := map[string]int{"c": 3, "b": 2, "a": 1}
	ea, err := Encode(a)
	if err != nil {
		t.Fatal(err)
	}
	eb, _ := Encode(b)
	if !bytes.Equal(ea, eb) {
		t.Fatalf("insertion order changed bytes: %x vs %x", ea, eb)
	}
	want := []byte{0xa3, 0x61, 'a', 0x01, 0x61, 'b', 0x02, 0x61, 'c', 0x03}
	if !bytes.Equal(ea, want) {
		t.Fatalf("golden: got %x want %x", ea, want)
	}
}

func TestAppendScanRoundTrip(t *testing.T) {
	ctx := context.Background()
	f, err := Open(filepath.Join(t.TempDir(), "log"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	for name, w := range map[string]interface {
		Writer
		Reader
	}{"mem": &MemLog{}, "file": f} {
		in := []Record{block(t, 1, 7), {At: 5, Class: Decision, Kind: "X", Actor: 7, Body: []byte{1, 2}}}
		if err := w.Append(ctx, in...); err != nil {
			t.Fatal(name, err)
		}
		var got []Record
		if err := w.Scan(ctx, 1, func(r Record) error { got = append(got, r); return nil }); err != nil {
			t.Fatal(name, err)
		}
		if len(got) != 2 || got[0].Seq != 1 || got[1].Seq != 2 || got[1].Kind != "X" ||
			got[1].Actor != 7 || got[1].At != 5 || !bytes.Equal(got[1].Body, []byte{1, 2}) {
			t.Fatalf("%s: round trip: %+v", name, got)
		}
	}
}

type sliceReader []Record

func (s sliceReader) Scan(ctx context.Context, from uint64, fn func(Record) error) error {
	return scanSlice(ctx, s, from, fn)
}

func TestSeqMonotone(t *testing.T) {
	ctx := context.Background()
	for name, recs := range map[string][]Record{
		"gap":        {{Seq: 1}, {Seq: 3}},
		"regression": {{Seq: 1}, {Seq: 2}, {Seq: 2}},
	} {
		err := sliceReader(recs).Scan(ctx, 1, func(Record) error { return nil })
		if !errors.Is(err, ErrSeqGap) {
			t.Fatalf("%s: scan err = %v", name, err)
		}
	}
	// A file with a gap is refused at Open, not truncated.
	var buf []byte
	for _, s := range []uint64{1, 3} {
		buf, _ = AppendFrame(buf, Record{Seq: s, Class: Input, Kind: "K"})
	}
	p := filepath.Join(t.TempDir(), "log")
	if err := os.WriteFile(p, buf, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(p); !errors.Is(err, ErrSeqGap) {
		t.Fatalf("Open err = %v", err)
	}
}

func TestTornTailTruncatedOnOpen(t *testing.T) {
	ctx := context.Background()
	p := filepath.Join(t.TempDir(), "log")
	f, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Append(ctx, block(t, 1, 1), block(t, 2, 1)); err != nil {
		t.Fatal(err)
	}
	if err := f.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	good, _ := os.ReadFile(p)

	frame, _ := AppendFrame(nil, Record{Seq: 3, Class: Input, Kind: "K", Body: bytes.Repeat([]byte{9}, 40)})
	flipped := append([]byte(nil), frame...)
	flipped[len(flipped)-1] ^= 0xff
	for name, tail := range map[string][]byte{
		"short header": frame[:3],
		"short body":   frame[:len(frame)-5],
		"bad checksum": flipped,
	} {
		if err := os.WriteFile(p, append(append([]byte(nil), good...), tail...), 0o600); err != nil {
			t.Fatal(err)
		}
		l, err := Open(p)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if l.LastSeq() != 2 {
			t.Fatalf("%s: LastSeq = %d", name, l.LastSeq())
		}
		if err := l.Append(ctx, block(t, 3, 1)); err != nil {
			t.Fatal(err)
		}
		if l.LastSeq() != 3 {
			t.Fatalf("%s: append after truncation: LastSeq = %d", name, l.LastSeq())
		}
		_ = l.Close()
		st, _ := os.Stat(p)
		if st.Size() < int64(len(good)) {
			t.Fatalf("%s: committed entries lost", name)
		}
	}

	// A bad frame in the middle is corruption, not a torn tail.
	mid := append([]byte(nil), good...)
	mid[frameHead+2] ^= 0xff
	if err := os.WriteFile(p, mid, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(p); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("mid corruption: err = %v", err)
	}
}

func TestSyncAdvancesDurableSeq(t *testing.T) {
	ctx := context.Background()
	f, err := Open(filepath.Join(t.TempDir(), "log"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	_ = f.Append(ctx, block(t, 1, 1), block(t, 2, 1))
	if f.LastSeq() != 2 || f.DurableSeq() != 0 {
		t.Fatalf("before sync: last %d durable %d", f.LastSeq(), f.DurableSeq())
	}
	_ = f.Sync(ctx)
	if f.DurableSeq() != 2 {
		t.Fatalf("after sync: durable %d", f.DurableSeq())
	}
}

func TestTruncateAfter(t *testing.T) {
	ctx := context.Background()
	p := filepath.Join(t.TempDir(), "log")
	f, _ := Open(p)
	_ = f.Append(ctx, block(t, 1, 1), block(t, 2, 1), block(t, 3, 1))
	_ = f.Sync(ctx)
	if err := f.TruncateAfter(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if f.LastSeq() != 1 || f.DurableSeq() != 1 {
		t.Fatalf("last %d durable %d", f.LastSeq(), f.DurableSeq())
	}
	_ = f.Append(ctx, block(t, 9, 1))
	_ = f.Close()
	g, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = g.Close() }()
	var slots []uint64
	_ = g.Scan(ctx, 1, func(r Record) error {
		var b BlockApplied
		_ = Decode(r.Body, &b)
		slots = append(slots, b.Point.Slot)
		return nil
	})
	if len(slots) != 2 || slots[0] != 1 || slots[1] != 9 {
		t.Fatalf("slots = %v", slots)
	}
}

func TestScanTailFromSeq(t *testing.T) {
	ctx := context.Background()
	f, _ := Open(filepath.Join(t.TempDir(), "log"))
	defer func() { _ = f.Close() }()
	for name, w := range map[string]interface {
		Writer
		Reader
	}{"mem": &MemLog{}, "file": f} {
		for i := uint64(1); i <= 10; i++ {
			_ = w.Append(ctx, block(t, i, 1))
		}
		const ckpt = 6
		var got []uint64
		_ = w.Scan(ctx, ckpt+1, func(r Record) error { got = append(got, r.Seq); return nil })
		if len(got) != 4 || got[0] != 7 || got[3] != 10 {
			t.Fatalf("%s: tail = %v", name, got)
		}
		got = nil
		_ = w.Scan(ctx, 11, func(r Record) error { got = append(got, r.Seq); return nil })
		if len(got) != 0 {
			t.Fatalf("%s: past end = %v", name, got)
		}
	}
}

func TestReplayReproduces(t *testing.T) {
	log := &MemLog{}
	live := drive(t, session(t, log), 500)
	res, err := (&Replayer{New: newToy, Log: log}).Replay(context.Background(), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Diff) != 0 {
		t.Fatalf("diff: %v", res.Diff[0])
	}
	if !bytes.Equal(res.State, live) {
		t.Fatal("replayed state differs from live state")
	}
	// Byte-identical output: a second run of the same inputs gives the same log.
	log2 := &MemLog{}
	drive(t, session(t, log2), 500)
	if !equalLogs(t, log, log2) {
		t.Fatal("same inputs gave different logs")
	}
}

func equalLogs(t *testing.T, a, b *MemLog) bool {
	t.Helper()
	var ea, eb []byte
	for _, p := range []struct {
		l *MemLog
		o *[]byte
	}{{a, &ea}, {b, &eb}} {
		_ = p.l.Scan(context.Background(), 1, func(r Record) error {
			*p.o, _ = AppendFrame(*p.o, r)
			return nil
		})
	}
	return bytes.Equal(ea, eb)
}

func TestReplayDetectsDrift(t *testing.T) {
	log := &MemLog{}
	drive(t, session(t, log), 100)
	var recs []Record
	_ = log.Scan(context.Background(), 1, func(r Record) error { recs = append(recs, r); return nil })
	// Change the actor of the first BlockApplied input.
	var driftSeq uint64
	for i := range recs {
		if recs[i].Kind == KindBlockApplied {
			recs[i].Actor += 5
			driftSeq = recs[i].Seq
			break
		}
	}
	d, err := (&Replayer{New: newToy, Log: sliceReader(recs)}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(d) == 0 {
		t.Fatal("drift not detected")
	}
	if d[0].Seq != driftSeq+1 {
		t.Fatalf("first mismatch at seq %d, want %d (the decision after the flipped input)", d[0].Seq, driftSeq+1)
	}
}

func TestHeaderMismatchRefused(t *testing.T) {
	ctx := context.Background()
	h := HeaderBody{SeedFingerprint: []byte{1}, ProfileHash: []byte{2}, GenesisHash: []byte{3}, BinaryVersion: "v1"}
	body, _ := Encode(h)
	log := &MemLog{}
	_ = log.Append(ctx, Record{Class: Header, Kind: KindHeader, Body: body})
	built := false
	factory := func(s []byte) (Engine, error) { built = true; return newToy(s) }

	if _, err := (&Replayer{New: factory, Log: log, Header: &h}).Run(ctx); err != nil {
		t.Fatal(err)
	}
	bad := h
	bad.ProfileHash = []byte{9}
	built = false
	_, err := (&Replayer{New: factory, Log: log, Header: &bad}).Run(ctx)
	if !errors.Is(err, ErrHeaderMismatch) || built {
		t.Fatalf("err = %v, engine built = %v", err, built)
	}
	_, err = (&Replayer{New: factory, Log: &MemLog{}, Header: &h}).Run(ctx)
	if !errors.Is(err, ErrHeaderMismatch) {
		t.Fatalf("headerless: err = %v", err)
	}
}

func TestNoSecretsInLog(t *testing.T) {
	log := &MemLog{}
	drive(t, session(t, log), 200)
	secret := bytes.Repeat([]byte{0xA5, 0x5A, 0xC3}, 11)
	if seq, err := LeaksSecret(context.Background(), log, secret); err != nil || seq != 0 {
		t.Fatalf("clean log: seq %d err %v", seq, err)
	}
	_ = log.Append(context.Background(), Record{Class: Input, Kind: "Control", Body: append([]byte{1}, secret...)})
	if seq, _ := LeaksSecret(context.Background(), log, secret); seq != log.LastSeq() {
		t.Fatalf("planted secret not found, seq %d", seq)
	}
}

func TestReplayFromCheckpointEqualsFullReplay(t *testing.T) {
	ctx := context.Background()
	log := &MemLog{}
	s := session(t, log)
	drive(t, s, 120)
	// Checkpoint at the last log entry that ends an input's decisions.
	ckptSeq := log.LastSeq()
	ckptState, _ := s.Eng.Snapshot()
	// More inputs after the checkpoint (drive restarts its tip counter, so
	// feed through the same session directly).
	for i := uint64(0); i < 40; i++ {
		if err := s.Feed(ctx, block(t, 1000+i, i%3)); err != nil {
			t.Fatal(err)
		}
	}
	rp := &Replayer{New: newToy, Log: log}
	full, err := rp.Replay(ctx, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	tail, err := rp.Replay(ctx, ckptState, ckptSeq)
	if err != nil {
		t.Fatal(err)
	}
	if len(full.Diff) != 0 || len(tail.Diff) != 0 {
		t.Fatalf("diffs: full %d tail %d", len(full.Diff), len(tail.Diff))
	}
	if !bytes.Equal(full.State, tail.State) || full.Last != tail.Last {
		t.Fatal("checkpoint + tail differs from full replay")
	}
}

func TestRollbackRecordReplays(t *testing.T) {
	ctx := context.Background()
	log := &MemLog{}
	s := session(t, log)
	for _, r := range []Record{
		block(t, 1, 1), block(t, 2, 1), block(t, 3, 2), rollback(t, 1), block(t, 4, 2),
	} {
		if err := s.Feed(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	live, _ := s.Eng.Snapshot()
	res, err := (&Replayer{New: newToy, Log: log}).Replay(ctx, nil, 0)
	if err != nil || len(res.Diff) != 0 || !bytes.Equal(res.State, live) {
		t.Fatalf("err %v diff %v equal %v", err, res.Diff, bytes.Equal(res.State, live))
	}
	var st toyState
	_ = Decode(res.State, &st)
	// Actor 1 keeps block 1 only; actor 2 lost block 3 then gained block 4.
	want := []toyCounter{{1, 1}, {2, 1}}
	if len(st.Counters) != 2 || st.Counters[0] != want[0] || st.Counters[1] != want[1] {
		t.Fatalf("counters = %+v", st.Counters)
	}
}

func TestIntersectionRollbackIsLogged(t *testing.T) {
	ctx := context.Background()
	log := &MemLog{}
	s := session(t, log)
	for i := uint64(1); i <= 4; i++ {
		_ = s.Feed(ctx, block(t, i, 1))
	}
	before := log.LastSeq()
	pt := func(slot uint64) ChainPoint {
		return ChainPoint{Slot: slot, Hash: binary.BigEndian.AppendUint64(nil, slot)}
	}
	wrote, err := s.Intersect(ctx, 0, pt(4), pt(2))
	if err != nil || !wrote {
		t.Fatalf("wrote %v err %v", wrote, err)
	}
	_ = s.Feed(ctx, block(t, 5, 1))
	var kinds []string
	_ = log.Scan(ctx, before+1, func(r Record) error {
		if r.Class == Input {
			kinds = append(kinds, r.Kind)
		}
		return nil
	})
	if len(kinds) != 2 || kinds[0] != KindRolledBack || kinds[1] != KindBlockApplied {
		t.Fatalf("inputs after restart = %v", kinds)
	}
	if log.DurableSeq() < before+1 {
		t.Fatal("RolledBack input not synced before its effect")
	}
	if wrote, _ := s.Intersect(ctx, 0, pt(5), pt(5)); wrote {
		t.Fatal("intersection at the tip wrote a record")
	}
	if _, err := s.Intersect(ctx, 0, pt(5), pt(9)); !errors.Is(err, ErrIntersectAhead) {
		t.Fatalf("ahead: err = %v", err)
	}
}

func TestClockReadsAreLogged(t *testing.T) {
	ctx := context.Background()
	log := &MemLog{}
	s := session(t, log)
	_ = s.Tick(ctx, 777)
	_ = s.Feed(ctx, block(t, 1, 1))
	var ticks int
	var recs []Record
	_ = log.Scan(ctx, 1, func(r Record) error {
		recs = append(recs, r)
		if r.Kind == KindTick && r.At == 777 {
			ticks++
		}
		return nil
	})
	if ticks != 1 {
		t.Fatalf("ticks logged = %d", ticks)
	}
	// Drop the Tick and renumber: the decision that read the clock drifts.
	var without []Record
	for _, r := range recs {
		if r.Kind != KindTick {
			r.Seq = uint64(len(without)) + 1
			without = append(without, r)
		}
	}
	d, err := (&Replayer{New: newToy, Log: sliceReader(without)}).Run(ctx)
	if err != nil || len(d) == 0 {
		t.Fatalf("diff without Tick: %v err %v", d, err)
	}
	d, _ = (&Replayer{New: newToy, Log: sliceReader(recs)}).Run(ctx)
	if len(d) != 0 {
		t.Fatal("diff with Tick")
	}
}

func TestFeedSyncsInputBeforeHandle(t *testing.T) {
	log := &MemLog{}
	var seen uint64 = 99
	eng := &probe{onHandle: func() { seen = log.DurableSeq() }}
	s := &Session{Eng: eng, Log: log}
	if err := s.Feed(context.Background(), Record{Kind: KindControl}); err != nil {
		t.Fatal(err)
	}
	if seen != 1 {
		t.Fatalf("durable seq when engine ran = %d, want 1", seen)
	}
}

type probe struct{ onHandle func() }

func (p *probe) Handle(context.Context, Record) ([]Record, error) { p.onHandle(); return nil, nil }
func (p *probe) Snapshot() ([]byte, error)                        { return nil, nil }

func TestApplyPointRebuildsIntersectionPoints(t *testing.T) {
	var pts []ChainPoint
	var err error
	for _, r := range []Record{block(t, 1, 1), block(t, 2, 1), block(t, 3, 1), rollback(t, 1), block(t, 4, 1)} {
		if pts, err = ApplyPoint(pts, r); err != nil {
			t.Fatal(err)
		}
	}
	if len(pts) != 2 || pts[0].Slot != 1 || pts[1].Slot != 4 {
		t.Fatalf("points = %+v", pts)
	}
	if !Retains(block(t, 1, 1)) || Retains(Record{Class: Input, Kind: KindTick}) {
		t.Fatal("Retains wrong")
	}
}

func FuzzDecodeFrame(f *testing.F) {
	for _, r := range []Record{block(f, 1, 1), rollback(f, 3), {Class: Decision, Kind: "K", Body: []byte{1}}} {
		r.Seq = 1
		b, _ := AppendFrame(nil, r)
		f.Add(b)
	}
	f.Add([]byte{0xff, 0xff, 0xff, 0xff, 0, 0, 0, 0, 1})
	f.Fuzz(func(t *testing.T, b []byte) {
		r, n, err := DecodeFrame(b)
		if err != nil {
			return
		}
		// Oracle: whatever decodes re-encodes to the bytes it consumed.
		again, err := AppendFrame(nil, r)
		if err != nil {
			t.Fatalf("re-encode: %v", err)
		}
		r2, n2, err := DecodeFrame(again)
		if err != nil || n2 != len(again) || n > len(b) {
			t.Fatalf("round trip: %v n=%d n2=%d", err, n, n2)
		}
		if r2.Seq != r.Seq || r2.Kind != r.Kind || !bytes.Equal(r2.Body, r.Body) {
			t.Fatal("round trip changed the record")
		}
	})
}
