package sink

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/apache/arrow-go/v18/parquet/file"
	"github.com/apache/arrow-go/v18/parquet/pqarrow"
	"go.uber.org/goleak"

	"github.com/chrisguiney/cardano-continuum/internal/agenda"
	"github.com/chrisguiney/cardano-continuum/internal/engine"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
)

func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }

func enc(t testing.TB, v any) []byte {
	t.Helper()
	b, err := eventlog.Encode(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func tx(b byte) engine.TxID { return engine.TxID(bytes.Repeat([]byte{b}, 32)) }

// fixture is a log touching every table: two attempts of one payment, a
// rejection with two failure classes, a confirmation, blocks and a rollback.
func fixture(t testing.TB) []eventlog.Record {
	t.Helper()
	pt := func(b byte) eventlog.ChainPoint {
		return eventlog.ChainPoint{Slot: uint64(b), Hash: bytes.Repeat([]byte{b}, 32)}
	}
	dec := agenda.Decision{
		Intent: agenda.Intent{Kind: "send", Actor: 1, Target: 2},
		Scores: []agenda.Score{{Kind: "idle", Value: 0.25, Why: "base"}, {Kind: "send", Value: 0.75, Target: 2, Why: "rich"}},
	}
	recs := []eventlog.Record{
		{Class: eventlog.Header, Kind: eventlog.KindHeader, Body: enc(t, eventlog.HeaderBody{
			SeedFingerprint: []byte{1, 2}, ProfileHash: []byte{3}, GenesisHash: []byte{4}, BinaryVersion: "m1"})},
		{At: 0, Class: eventlog.Decision, Kind: engine.KindSpawned, Actor: 1, Body: enc(t, engine.Spawned{Profile: "hodl"})},
		{At: 5, Class: eventlog.Input, Kind: eventlog.KindTick},
		{At: 5, Class: eventlog.Decision, Kind: engine.KindTransition, Actor: 1, Body: enc(t, engine.Transition{From: "new", To: "active"})},
		{At: 6, Class: eventlog.Decision, Kind: engine.KindIntentChosen, Actor: 1, Body: enc(t, dec)},
		{At: 6, Class: eventlog.Decision, Kind: engine.KindSubmitted, Actor: 1, Body: enc(t, engine.Submitted{Intent: 9, ID: tx(0xa1), To: 2, Amount: 5_000_000, Fee: 170000, TTL: 100})},
		{At: 7, Class: eventlog.Input, Kind: eventlog.KindSubmitResult, Body: enc(t, engine.SubmitResult{Intent: 9, Verdict: engine.VerdictRejected, Reason: "bad", Failures: []string{"FeeTooSmall", "BadInputs"}})},
		{At: 8, Class: eventlog.Decision, Kind: engine.KindRefused, Actor: 1, Body: enc(t, engine.Refused{Intent: 9, ID: tx(0xa1), Why: "bad"})},
		{At: 9, Class: eventlog.Decision, Kind: engine.KindSubmitted, Actor: 1, Body: enc(t, engine.Submitted{Intent: 9, ID: tx(0xa2), To: 2, Amount: 5_000_000, Fee: 180000, TTL: 120})},
		{At: 9, Class: eventlog.Input, Kind: eventlog.KindSubmitResult, Body: enc(t, engine.SubmitResult{Intent: 9, Verdict: engine.VerdictAccepted})},
		{At: 10, Class: eventlog.Input, Kind: eventlog.KindBlockApplied, Body: enc(t, eventlog.BlockApplied{Point: pt(10)})},
		{At: 11, Class: eventlog.Input, Kind: eventlog.KindBlockApplied, Body: enc(t, eventlog.BlockApplied{Point: pt(11)})},
		{At: 12, Class: eventlog.Input, Kind: eventlog.KindRolledBack, Body: enc(t, eventlog.RolledBack{To: pt(10)})},
		{At: 13, Class: eventlog.Decision, Kind: engine.KindConfirmed, Actor: 1, Body: enc(t, engine.Confirmed{Intent: 9, ID: tx(0xa2)})},
		{At: 14, Class: eventlog.Decision, Kind: engine.KindGaveUp, Actor: 1, Body: enc(t, engine.GaveUp{Intent: 10})},
	}
	for i := range recs {
		recs[i].Seq = uint64(i + 1)
	}
	return recs
}

func writeLog(t testing.TB, path string, recs []eventlog.Record) {
	t.Helper()
	l, err := eventlog.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Append(context.Background(), recs...); err != nil {
		t.Fatal(err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
}

// dump renders every row of a table, in part order, as strings.
func dump(t testing.TB, dir, table string) []string {
	t.Helper()
	parts, err := filepath.Glob(filepath.Join(dir, table, "part-*.parquet"))
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(parts)
	var rows []string
	for _, p := range parts {
		rows = append(rows, readPart(t, p)...)
	}
	return rows
}

func readPart(t testing.TB, path string) []string {
	t.Helper()
	rdr, err := file.OpenParquetFile(path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rdr.Close() }()
	fr, err := pqarrow.NewFileReader(rdr, pqarrow.ArrowReadProperties{}, memory.DefaultAllocator)
	if err != nil {
		t.Fatal(err)
	}
	tbl, err := fr.ReadTable(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tbl.Release()
	var rows []string
	for i := range int(tbl.NumRows()) {
		var row string
		for c := range int(tbl.NumCols()) {
			row += fmt.Sprintf("%s|", cellAt(tbl.Column(c).Data().Chunks(), i))
		}
		rows = append(rows, row)
	}
	return rows
}

func cellAt(chunks []arrow.Array, i int) string {
	for _, c := range chunks {
		if i < c.Len() {
			if c.IsNull(i) {
				return "null"
			}
			if f, ok := c.(*array.FixedSizeBinary); ok {
				return fmt.Sprintf("%x", f.Value(i))
			}
			return c.ValueStr(i)
		}
		i -= c.Len()
	}
	return "?"
}

func export(t testing.TB, logPath, dir string, opts Options) Stats {
	t.Helper()
	st, err := Export(context.Background(), logPath, dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestExportRows(t *testing.T) {
	d := t.TempDir()
	lp := filepath.Join(d, "events.log")
	writeLog(t, lp, fixture(t))
	out := filepath.Join(d, "out")
	st := export(t, lp, out, Options{})
	if st.LastSeq != 15 {
		t.Fatalf("last seq = %d", st.LastSeq)
	}
	want := map[string]int{
		tDecision: 14, tScore: 2, tSubmission: 2, tResult: 2, tFailure: 2,
		tConfirm: 1, tTransition: 2, tBlock: 2, tRollback: 1, tRun: 1,
	}
	for tbl, n := range want {
		if got := len(dump(t, out, tbl)); got != n {
			t.Errorf("%s: %d rows, want %d", tbl, got, n)
		}
	}
	if got := dump(t, out, tSubmission); got[1] != "9|9|1|9|2|"+hex32(0xa2)+"|2|5000000|180000|120|" {
		t.Errorf("submission row = %q", got[1])
	}
	if got := dump(t, out, tResult); got[1] != "10|9|9|2|"+hex32(0xa2)+"|accepted|null|false|" {
		t.Errorf("result row = %q", got[1])
	}
	if got := dump(t, out, tDecision); got[3] != "5|6|Decision|1|IntentChosen|null|send|0.75|" {
		t.Errorf("chosen row = %q", got[3])
	}
	if got := dump(t, out, tFailure); got[1] != "7|1|BadInputs|" {
		t.Errorf("failure row = %q", got[1])
	}
}

func hex32(b byte) string { return fmt.Sprintf("%x", bytes.Repeat([]byte{b}, 32)) }

func TestSubmissionHashesAre32Bytes(t *testing.T) {
	d := t.TempDir()
	lp := filepath.Join(d, "events.log")
	writeLog(t, lp, fixture(t))
	out := filepath.Join(d, "out")
	export(t, lp, out, Options{})
	parts, _ := filepath.Glob(filepath.Join(out, tSubmission, "part-*.parquet"))
	rdr, err := file.OpenParquetFile(parts[0], false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rdr.Close() }()
	c := rdr.MetaData().Schema.Column(5)
	if c.TypeLength() != 32 || c.Name() != "tx_hash" {
		t.Fatalf("%s type length %d", c.Name(), c.TypeLength())
	}
}

func TestIncrementalEqualsOneShot(t *testing.T) {
	recs := fixture(t)
	d := t.TempDir()
	one := filepath.Join(d, "one")
	full := filepath.Join(d, "full.log")
	writeLog(t, full, recs)
	export(t, full, one, Options{})
	for cut := 1; cut < len(recs); cut++ {
		inc := filepath.Join(d, fmt.Sprintf("inc%d", cut))
		lp := filepath.Join(d, fmt.Sprintf("cut%d.log", cut))
		writeLog(t, lp, recs[:cut])
		export(t, lp, inc, Options{})
		if err := os.Remove(lp); err != nil {
			t.Fatal(err)
		}
		writeLog(t, lp, recs)
		st := export(t, lp, inc, Options{})
		if st.FromSeq != uint64(cut)+1 {
			t.Fatalf("cut %d: from seq %d", cut, st.FromSeq)
		}
		for _, tbl := range TableNames {
			if !slices.Equal(dump(t, inc, tbl), dump(t, one, tbl)) {
				t.Fatalf("cut %d: table %s differs", cut, tbl)
			}
		}
	}
}

func TestRerunWritesNothing(t *testing.T) {
	d := t.TempDir()
	lp := filepath.Join(d, "events.log")
	writeLog(t, lp, fixture(t))
	out := filepath.Join(d, "out")
	export(t, lp, out, Options{})
	before, _ := os.ReadFile(filepath.Join(out, ManifestName))
	st := export(t, lp, out, Options{})
	after, _ := os.ReadFile(filepath.Join(out, ManifestName))
	if !bytes.Equal(before, after) || len(st.Rows) != 0 {
		t.Fatalf("rerun changed the manifest or wrote rows: %v", st.Rows)
	}
}

func TestCrashKeepsPreviousManifest(t *testing.T) {
	recs := fixture(t)
	d := t.TempDir()
	lp := filepath.Join(d, "events.log")
	out := filepath.Join(d, "out")
	writeLog(t, lp, recs[:8])
	export(t, lp, out, Options{})
	before, err := os.ReadFile(filepath.Join(out, ManifestName))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(lp); err != nil {
		t.Fatal(err)
	}
	writeLog(t, lp, recs)

	boom := errors.New("boom")
	for _, stage := range []string{StagePartsRenamed, StageManifestTmp} {
		_, err = Export(context.Background(), lp, out, Options{Fault: func(s string) error {
			if s == stage {
				return boom
			}
			return nil
		}})
		if !errors.Is(err, boom) {
			t.Fatalf("%s: err = %v", stage, err)
		}
		after, _ := os.ReadFile(filepath.Join(out, ManifestName))
		if !bytes.Equal(before, after) {
			t.Fatalf("%s: manifest changed", stage)
		}
	}

	// A fresh run removes orphans and converges on the one-shot result.
	export(t, lp, out, Options{})
	one := filepath.Join(d, "one")
	export(t, lp, one, Options{})
	for _, tbl := range TableNames {
		if !slices.Equal(dump(t, out, tbl), dump(t, one, tbl)) {
			t.Errorf("table %s differs after recovery", tbl)
		}
	}
	for _, p := range parquetFiles(t, out) {
		if filepath.Base(p)[0] == '.' {
			t.Errorf("temp file left: %s", p)
		}
	}
}

func parquetFiles(t testing.TB, dir string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(dir, func(p string, e os.DirEntry, err error) error {
		if err == nil && !e.IsDir() && filepath.Base(p) != ManifestName {
			out = append(out, p)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestRejectsShortBlockHash(t *testing.T) {
	recs := fixture(t)
	recs[10].Body = enc(t, eventlog.BlockApplied{Point: eventlog.ChainPoint{Slot: 1, Hash: []byte{1}}})
	d := t.TempDir()
	lp := filepath.Join(d, "events.log")
	writeLog(t, lp, recs)
	if _, err := Export(context.Background(), lp, filepath.Join(d, "out"), Options{}); err == nil {
		t.Fatal("want error")
	}
}

func TestRefusesDifferentRun(t *testing.T) {
	recs := fixture(t)
	d := t.TempDir()
	lp := filepath.Join(d, "a.log")
	writeLog(t, lp, recs)
	out := filepath.Join(d, "out")
	export(t, lp, out, Options{})
	other := fixture(t)
	other[0].Body = enc(t, eventlog.HeaderBody{SeedFingerprint: []byte{9}, ProfileHash: []byte{3}, GenesisHash: []byte{4}, BinaryVersion: "m1"})
	lp2 := filepath.Join(d, "b.log")
	writeLog(t, lp2, other)
	if _, err := Export(context.Background(), lp2, out, Options{}); err == nil {
		t.Fatal("want header mismatch error")
	}
}

func TestTornTailIgnored(t *testing.T) {
	recs := fixture(t)
	d := t.TempDir()
	lp := filepath.Join(d, "events.log")
	writeLog(t, lp, recs)
	f, err := os.OpenFile(lp, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte{0, 0, 1, 0, 9}); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	st := export(t, lp, filepath.Join(d, "out"), Options{})
	if st.LastSeq != uint64(len(recs)) {
		t.Fatalf("last seq = %d", st.LastSeq)
	}
}
