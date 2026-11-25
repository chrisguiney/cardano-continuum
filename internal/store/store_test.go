package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"

	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

const depthK = 2

// counter is a deterministic engine: the chain points it has seen.
type counter struct {
	S struct {
		Points []eventlog.ChainPoint `cbor:"1,keyasint"`
	}
}

func newCounter(snap []byte) (eventlog.Engine, error) {
	c := &counter{}
	if snap != nil {
		if err := eventlog.Decode(snap, &c.S); err != nil {
			return nil, err
		}
	}
	return c, nil
}

func (c *counter) Handle(_ context.Context, in eventlog.Record) ([]eventlog.Record, error) {
	p, err := eventlog.ApplyPoint(c.S.Points, in)
	c.S.Points = p
	return nil, err
}

func (c *counter) Snapshot() ([]byte, error) { return eventlog.Encode(c.S) }

func blockRec(slot uint64) eventlog.Record {
	body, _ := eventlog.Encode(eventlog.BlockApplied{Point: pt(slot)})
	return eventlog.Record{At: sched.Time(slot), Class: eventlog.Input, Kind: eventlog.KindBlockApplied, Body: body}
}

func rollRec(slot uint64) eventlog.Record {
	body, _ := eventlog.Encode(eventlog.RolledBack{To: pt(slot)})
	return eventlog.Record{At: sched.Time(slot), Class: eventlog.Input, Kind: eventlog.KindRolledBack, Body: body}
}

func pt(slot uint64) eventlog.ChainPoint {
	return eventlog.ChainPoint{Slot: slot, Hash: []byte{byte(slot), 0xab}}
}

func openDB(t *testing.T, dir string, log DurableLog, opts ...Option) *DB {
	t.Helper()
	if log != nil {
		opts = append(opts, WithLog(log))
	}
	d, err := Open(context.Background(), filepath.Join(dir, "c.db"), opts...)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return d
}

// rig is a live engine over a file log, checkpointing into a store.
type rig struct {
	t    *testing.T
	dir  string
	log  *eventlog.FileLog
	db   *DB
	sess *eventlog.Session
	eng  eventlog.Engine
}

func newRig(t *testing.T, dir string, opts ...Option) *rig {
	t.Helper()
	log, err := eventlog.Open(filepath.Join(dir, "log"))
	if err != nil {
		t.Fatal(err)
	}
	db := openDB(t, dir, log, opts...)
	eng, _ := newCounter(nil)
	return &rig{t: t, dir: dir, log: log, db: db, eng: eng, sess: &eventlog.Session{Eng: eng, Log: log}}
}

func (r *rig) feed(rec eventlog.Record) {
	r.t.Helper()
	if err := r.sess.Feed(context.Background(), rec); err != nil {
		r.t.Fatal(err)
	}
}

func (r *rig) checkpoint() error {
	r.t.Helper()
	snap, err := r.eng.Snapshot()
	if err != nil {
		r.t.Fatal(err)
	}
	pts := r.eng.(*counter).S.Points
	conf := pts[max(0, len(pts)-1-depthK)]
	return r.db.SaveCheckpoint(context.Background(), Checkpoint{
		Seq: r.log.DurableSeq(), Points: pts, Confirmed: conf, Actors: snap,
		Intents: Blob{1, []byte("i")}, Reservations: Blob{1, []byte("r")},
	})
}

// crash drops every volatile object without a clean shutdown of the log.
func (r *rig) crash() {
	r.t.Helper()
	_ = r.db.Close()
	_ = r.log.Close()
}

// recoverState reopens both files and returns the engine state recovery yields.
func recoverState(t *testing.T, dir string) []byte {
	t.Helper()
	ctx := context.Background()
	log, err := eventlog.Open(filepath.Join(dir, "log"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close() }()
	db := openDB(t, dir, log)
	defer func() { _ = db.Close() }()
	rec, err := db.Recover(ctx)
	if err != nil && !errors.Is(err, ErrNoCheckpoint) {
		t.Fatalf("Recover: %v", err)
	}
	rp := &eventlog.Replayer{New: newCounter, Log: log}
	res, err := rp.Replay(ctx, rec.Checkpoint.Actors, rec.Checkpoint.Seq)
	if err != nil || len(res.Diff) != 0 {
		t.Fatalf("tail replay: %v %v", err, res.Diff)
	}
	return res.State
}

func fullReplayState(t *testing.T, dir string) []byte {
	t.Helper()
	log, err := eventlog.Open(filepath.Join(dir, "log"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = log.Close() }()
	res, err := (&eventlog.Replayer{New: newCounter, Log: log}).Replay(context.Background(), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	return res.State
}

func TestOpenMigrates(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	d := openDB(t, dir, nil)
	v, err := d.SchemaVersion(ctx)
	if err != nil || v != SchemaVersion {
		t.Fatalf("version %d, %v; want %d", v, err, SchemaVersion)
	}
	seed, err := d.Seed(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_ = d.Close()

	d = openDB(t, dir, nil)
	if v2, _ := d.SchemaVersion(ctx); v2 != v {
		t.Fatalf("reopen changed version %d -> %d", v, v2)
	}
	if s2, _ := d.Seed(ctx); s2.Root() != seed.Root() {
		t.Fatal("reopen lost data")
	}
	if _, err := d.db.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", SchemaVersion+1)); err != nil {
		t.Fatal(err)
	}
	_ = d.Close()

	if _, err := Open(ctx, filepath.Join(dir, "c.db")); !errors.Is(err, ErrSchemaTooNew) {
		t.Fatalf("newer schema: %v, want ErrSchemaTooNew", err)
	}
}

func TestSeedStable(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	d := openDB(t, dir, nil)
	a, err := d.Seed(ctx)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := d.Seed(ctx)
	if a.Root() != b.Root() || a.Root() == ([32]byte{}) {
		t.Fatal("seed not stable or zero")
	}
	_ = d.Close()
	d = openDB(t, dir, nil)
	defer func() { _ = d.Close() }()
	if c, _ := d.Seed(ctx); c.Root() != a.Root() {
		t.Fatal("seed changed across reopen")
	}
	raw := a.Root()
	for _, out := range []string{
		fmt.Sprint(a), fmt.Sprintf("%v %+v %#v %s %x", a, a, a, a, a), fmt.Sprint(&a),
	} {
		if bytes.Contains([]byte(out), raw[:4]) || bytes.Contains([]byte(out), []byte(fmt.Sprintf("%x", raw[:4]))) {
			t.Fatalf("seed leaked in %q", out)
		}
	}
	st, err := os.Stat(filepath.Join(dir, "c.db"))
	if err != nil || st.Mode().Perm()&0o077 != 0 {
		t.Fatalf("db file mode %v, %v; want no group/other access", st.Mode(), err)
	}
}

func TestCheckpointRestore(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	log := &eventlog.MemLog{}
	for i := range 3 {
		_ = log.Append(ctx, blockRec(uint64(i+1)))
	}
	_ = log.Sync(ctx)
	want := Checkpoint{
		Seq: 3, Points: []eventlog.ChainPoint{pt(1), pt(2), pt(3)}, Confirmed: pt(1),
		Actors:    []byte("actors"),
		ActorRows: []ActorRow{{1, "a", "hodl"}, {2, "b", "yield"}},
		Intents:   Blob{1, []byte("intents")}, Reservations: Blob{7, []byte("res")},
	}
	d := openDB(t, dir, log)
	if err := d.SaveCheckpoint(ctx, want); err != nil {
		t.Fatal(err)
	}
	_ = d.Close()
	d = openDB(t, dir, log)
	defer func() { _ = d.Close() }()
	got, err := d.LoadCheckpoint(ctx)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, %v\nwant %+v", got, err, want)
	}
}

func TestCheckpointSeqNotAheadOfLog(t *testing.T) {
	ctx := context.Background()
	log := &eventlog.MemLog{}
	_ = log.Append(ctx, blockRec(1), blockRec(2))
	_ = log.Sync(ctx)
	_ = log.Append(ctx, blockRec(3)) // appended, not durable
	d := openDB(t, t.TempDir(), log)
	defer func() { _ = d.Close() }()
	c := Checkpoint{Seq: 3, Points: []eventlog.ChainPoint{pt(1)}, Confirmed: pt(1)}
	if err := d.SaveCheckpoint(ctx, c); !errors.Is(err, ErrAheadOfLog) {
		t.Fatalf("seq 3 over durable 2: %v, want ErrAheadOfLog", err)
	}
	if _, err := d.LoadCheckpoint(ctx); !errors.Is(err, ErrNoCheckpoint) {
		t.Fatalf("refused save left a checkpoint: %v", err)
	}
	c.Seq = 2
	if err := d.SaveCheckpoint(ctx, c); err != nil {
		t.Fatalf("seq 2: %v", err)
	}
	nolog := openDB(t, t.TempDir(), nil)
	defer func() { _ = nolog.Close() }()
	if err := nolog.SaveCheckpoint(ctx, Checkpoint{Seq: 1}); !errors.Is(err, ErrNoLog) {
		t.Fatalf("no log bound: %v, want ErrNoLog", err)
	}
}

func TestCheckpointKeepsIntersectionPoints(t *testing.T) {
	ctx := context.Background()
	log := &eventlog.MemLog{}
	d := openDB(t, t.TempDir(), log)
	defer func() { _ = d.Close() }()
	pts := []eventlog.ChainPoint{pt(5), pt(6), pt(7), pt(8)}
	bad := Checkpoint{Points: pts, Confirmed: pt(3)}
	if err := d.SaveCheckpoint(ctx, bad); !errors.Is(err, ErrPointsShort) {
		t.Fatalf("confirmed below points: %v, want ErrPointsShort", err)
	}
	bad = Checkpoint{Confirmed: pt(3)}
	if err := d.SaveCheckpoint(ctx, bad); !errors.Is(err, ErrPointsShort) {
		t.Fatalf("no points: %v, want ErrPointsShort", err)
	}
	ok := Checkpoint{Points: pts, Confirmed: pt(6)}
	if err := d.SaveCheckpoint(ctx, ok); err != nil {
		t.Fatal(err)
	}
	got, _ := d.LoadCheckpoint(ctx)
	if !reflect.DeepEqual(got.Points, pts) || got.Points[0].Slot > got.Confirmed.Slot {
		t.Fatalf("points %v do not reach confirmed %v", got.Points, got.Confirmed)
	}
}

func TestRecoverRebuildsPoints(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	r := newRig(t, dir)
	for s := uint64(1); s <= 4; s++ {
		r.feed(blockRec(s))
	}
	if err := r.checkpoint(); err != nil {
		t.Fatal(err)
	}
	r.feed(blockRec(5))
	r.feed(rollRec(3))
	r.feed(blockRec(6))
	r.crash()

	log, _ := eventlog.Open(filepath.Join(dir, "log"))
	defer func() { _ = log.Close() }()
	db := openDB(t, dir, log)
	defer func() { _ = db.Close() }()
	rec, err := db.Recover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []eventlog.ChainPoint{pt(1), pt(2), pt(3), pt(6)}
	if !reflect.DeepEqual(rec.Points, want) || len(rec.Tail) != 3 || rec.Tail[0].Seq != rec.Checkpoint.Seq+1 {
		t.Fatalf("points %v tail %d; want %v and 3 tail records", rec.Points, len(rec.Tail), want)
	}
	// A checkpoint covering entries the log lost is refused, not guessed at.
	_ = log.TruncateAfter(ctx, 1)
	if _, err := db.Recover(ctx); !errors.Is(err, ErrCheckpointAhead) {
		t.Fatalf("log shorter than checkpoint: %v, want ErrCheckpointAhead", err)
	}
}

func TestCrashBetweenLogAndCheckpoint(t *testing.T) {
	dir := t.TempDir()
	r := newRig(t, dir)
	for s := uint64(1); s <= 3; s++ {
		r.feed(blockRec(s))
	}
	if err := r.checkpoint(); err != nil {
		t.Fatal(err)
	}
	r.feed(blockRec(4)) // log is durable and ahead; the crash comes before the checkpoint
	r.feed(rollRec(3))
	r.feed(blockRec(5))
	want, _ := r.eng.Snapshot()
	r.crash()

	if got := recoverState(t, dir); !bytes.Equal(got, want) {
		t.Fatalf("recovered state differs from uninterrupted run")
	}
}

func TestCrashAfterEveryWrite(t *testing.T) {
	// Each op is one durable commit: a log sync or a checkpoint transaction.
	type op struct {
		ckpt bool
		rec  eventlog.Record
	}
	ops := []op{
		{rec: blockRec(1)}, {rec: blockRec(2)}, {ckpt: true}, {rec: blockRec(3)},
		{rec: blockRec(4)}, {rec: rollRec(3)}, {ckpt: true}, {rec: blockRec(5)},
		{rec: blockRec(6)}, {ckpt: true}, {rec: blockRec(7)},
	}
	for n := 0; n <= len(ops); n++ {
		t.Run(strconv.Itoa(n), func(t *testing.T) {
			dir := t.TempDir()
			r := newRig(t, dir)
			for _, o := range ops[:n] {
				if o.ckpt {
					if err := r.checkpoint(); err != nil {
						t.Fatal(err)
					}
				} else {
					r.feed(o.rec)
				}
			}
			r.crash()
			got, want := recoverState(t, dir), fullReplayState(t, dir)
			if !bytes.Equal(got, want) {
				t.Fatalf("after %d writes recovery differs from full replay", n)
			}
		})
	}
}

// TestSaveCheckpointIsAtomic kills the process (os.Exit, no cleanup) at each
// stage inside SaveCheckpoint and checks the old checkpoint survives whole.
func TestSaveCheckpointIsAtomic(t *testing.T) {
	if dir := os.Getenv("STORE_CRASH_DIR"); dir != "" {
		crashChild(dir, os.Getenv("STORE_CRASH_STAGE"))
		return
	}
	for _, stage := range []string{"checkpoint", "points", "actors", "commit"} {
		t.Run(stage, func(t *testing.T) {
			dir := t.TempDir()
			ctx := context.Background()
			log := &eventlog.MemLog{}
			_ = log.Append(ctx, blockRec(1), blockRec(2))
			_ = log.Sync(ctx)
			old := Checkpoint{
				Seq: 1, Points: []eventlog.ChainPoint{pt(1)}, Confirmed: pt(1), Actors: []byte("old"),
				ActorRows: []ActorRow{{1, "old", "hodl"}},
				Intents:   Blob{1, []byte("old")}, Reservations: Blob{1, []byte("old")},
			}
			d := openDB(t, dir, log)
			if err := d.SaveCheckpoint(ctx, old); err != nil {
				t.Fatal(err)
			}
			_ = d.Close()

			cmd := exec.Command(os.Args[0], "-test.run=^TestSaveCheckpointIsAtomic$")
			cmd.Env = append(os.Environ(), "STORE_CRASH_DIR="+dir, "STORE_CRASH_STAGE="+stage)
			out, err := cmd.CombinedOutput()
			var ee *exec.ExitError
			if !errors.As(err, &ee) || ee.ExitCode() != 77 {
				t.Fatalf("child did not die at the failpoint: %v\n%s", err, out)
			}

			d = openDB(t, dir, log)
			defer func() { _ = d.Close() }()
			got, err := d.LoadCheckpoint(ctx)
			if err != nil || !reflect.DeepEqual(got, old) {
				t.Fatalf("after kill at %s: %+v, %v; want old checkpoint", stage, got, err)
			}
			rows, _ := d.Actors(ctx, ActorQuery{SortBy: "id", Limit: 10})
			if !reflect.DeepEqual(rows, old.ActorRows) {
				t.Fatalf("actor rows %v are not the old ones", rows)
			}
		})
	}
}

func crashChild(dir, stage string) {
	ctx := context.Background()
	log := &eventlog.MemLog{}
	_ = log.Append(ctx, blockRec(1), blockRec(2))
	_ = log.Sync(ctx)
	d, err := Open(ctx, filepath.Join(dir, "c.db"), WithLog(log), withFault(func(s string) error {
		if s == stage {
			os.Exit(77)
		}
		return nil
	}))
	if err != nil {
		os.Exit(1)
	}
	_ = d.SaveCheckpoint(ctx, Checkpoint{
		Seq: 2, Points: []eventlog.ChainPoint{pt(1), pt(2)}, Confirmed: pt(2), Actors: []byte("new"),
		ActorRows: []ActorRow{{1, "new", "yield"}, {2, "new2", "yield"}},
		Intents:   Blob{1, []byte("new")}, Reservations: Blob{1, []byte("new")},
	})
	os.Exit(0)
}

func TestSaveCheckpointFaultRollsBack(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("boom")
	for _, stage := range []string{"checkpoint", "points", "actors", "commit"} {
		d := openDB(t, t.TempDir(), &eventlog.MemLog{}, withFault(func(s string) error {
			if s == stage {
				return boom
			}
			return nil
		}))
		err := d.SaveCheckpoint(ctx, Checkpoint{Points: []eventlog.ChainPoint{pt(1)}, Confirmed: pt(1), Actors: []byte("x")})
		if !errors.Is(err, boom) {
			t.Fatalf("%s: %v", stage, err)
		}
		if _, err := d.LoadCheckpoint(ctx); !errors.Is(err, ErrNoCheckpoint) {
			t.Fatalf("%s: half checkpoint visible: %v", stage, err)
		}
		_ = d.Close()
	}
}

func TestActorsPagedSorted(t *testing.T) {
	ctx := context.Background()
	d := openDB(t, t.TempDir(), &eventlog.MemLog{})
	defer func() { _ = d.Close() }()
	rows := []ActorRow{{1, "c", "x"}, {2, "a", "z"}, {3, "b", "y"}}
	if err := d.SaveCheckpoint(ctx, Checkpoint{Points: []eventlog.ChainPoint{pt(1)}, Confirmed: pt(1), ActorRows: rows}); err != nil {
		t.Fatal(err)
	}
	got, err := d.Actors(ctx, ActorQuery{SortBy: "name", Limit: 2})
	if err != nil || !reflect.DeepEqual(got, []ActorRow{rows[1], rows[2]}) {
		t.Fatalf("name asc: %v %v", got, err)
	}
	got, _ = d.Actors(ctx, ActorQuery{SortBy: "role", Desc: true, Offset: 1, Limit: 5})
	if !reflect.DeepEqual(got, []ActorRow{rows[2], rows[0]}) {
		t.Fatalf("role desc offset: %v", got)
	}
	for _, key := range []string{"name; DROP TABLE actor", "bogus", ""} {
		if _, err := d.Actors(ctx, ActorQuery{SortBy: key, Limit: 1}); !errors.Is(err, ErrBadSort) {
			t.Fatalf("sort %q: %v, want ErrBadSort", key, err)
		}
	}
	if _, err := d.Actors(ctx, ActorQuery{SortBy: "id", Limit: -1}); err == nil {
		t.Fatal("negative limit accepted")
	}
}
