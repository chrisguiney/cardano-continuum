package export

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/apache/arrow-go/v18/parquet/file"
	"github.com/apache/arrow-go/v18/parquet/pqarrow"

	"github.com/chrisguiney/cardano-continuum/internal/engine"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/sink"
)

// fakeLog records a small fake run and returns its log path.
func fakeLog(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "run")
	var out bytes.Buffer
	args := []string{"run", "--fake", "--seed", "7", "--wallets", "20", "--epochs", "1", "--out", dir}
	if err := run(context.Background(), args, &out); err != nil {
		t.Fatalf("run: %v\n%s", err, out.String())
	}
	return filepath.Join(dir, "events.log")
}

func readLog(t *testing.T, path string) []eventlog.Record {
	t.Helper()
	l, err := eventlog.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	var recs []eventlog.Record
	err = l.Scan(context.Background(), 1, func(r eventlog.Record) error {
		recs = append(recs, r)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return recs
}

func tableRows(t *testing.T, dir, table string) int64 {
	t.Helper()
	parts, err := filepath.Glob(filepath.Join(dir, table, "part-*.parquet"))
	if err != nil {
		t.Fatal(err)
	}
	var n int64
	for _, p := range parts {
		r, err := file.OpenParquetFile(p, false)
		if err != nil {
			t.Fatal(err)
		}
		n += r.NumRows()
		_ = r.Close()
	}
	return n
}

func TestExportRoundTrip(t *testing.T) {
	logPath := fakeLog(t)
	out := filepath.Join(t.TempDir(), "pq")
	var buf bytes.Buffer
	if err := run(context.Background(), []string{"export", logPath, "--out", out}, &buf); err != nil {
		t.Fatalf("export: %v", err)
	}
	counts := map[string]int64{}
	for _, r := range readLog(t, logPath) {
		counts[r.Kind]++
	}
	if counts[engine.KindSubmitted] == 0 || counts[eventlog.KindBlockApplied] == 0 {
		t.Fatalf("fake run is too small to test: %v", counts)
	}
	for kind, want := range counts {
		if kind == eventlog.KindHeader {
			continue
		}
		if got := countKind(t, out, kind); got != want {
			t.Errorf("kind %s: %d rows, log has %d", kind, got, want)
		}
	}
	if got := tableRows(t, out, "submission"); got != counts[engine.KindSubmitted] {
		t.Errorf("submission rows = %d, want %d", got, counts[engine.KindSubmitted])
	}
	if got := tableRows(t, out, "block"); got != counts[eventlog.KindBlockApplied] {
		t.Errorf("block rows = %d, want %d", got, counts[eventlog.KindBlockApplied])
	}
	if got := tableRows(t, out, "run"); got != 1 {
		t.Errorf("run rows = %d", got)
	}
}

func TestExportIncrementalAppends(t *testing.T) {
	logPath := fakeLog(t)
	recs := readLog(t, logPath)
	dir := t.TempDir()
	short := filepath.Join(dir, "events.log")
	l, err := eventlog.Open(short)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Append(context.Background(), recs[:len(recs)/2]...); err != nil {
		t.Fatal(err)
	}
	inc := filepath.Join(dir, "inc")
	var buf bytes.Buffer
	if err := run(context.Background(), []string{"export", short, "--out", inc}, &buf); err != nil {
		t.Fatal(err)
	}
	if err := l.Append(context.Background(), recs[len(recs)/2:]...); err != nil {
		t.Fatal(err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if err := run(context.Background(), []string{"export", short, "--out", inc}, &buf); err != nil {
		t.Fatal(err)
	}
	one := filepath.Join(dir, "one")
	if err := run(context.Background(), []string{"export", logPath, "--out", one}, &buf); err != nil {
		t.Fatal(err)
	}
	for _, name := range sink.TableNames {
		if a, b := tableRows(t, inc, name), tableRows(t, one, name); a != b {
			t.Errorf("table %s: incremental %d rows, one-shot %d", name, a, b)
		}
	}
	if _, err := os.Stat(filepath.Join(inc, sink.ManifestName)); err != nil {
		t.Fatal(err)
	}
}

func TestExportUsage(t *testing.T) {
	var buf bytes.Buffer
	for _, args := range [][]string{{"export"}, {"export", "x.log"}, {"export", "--out", "d"}} {
		if err := run(context.Background(), args, &buf); err == nil {
			t.Errorf("%v: want usage error", args)
		}
	}
}

// countKind counts the decision-table rows of one log kind.
func countKind(t *testing.T, dir, kind string) int64 {
	t.Helper()
	parts, err := filepath.Glob(filepath.Join(dir, "decision", "part-*.parquet"))
	if err != nil {
		t.Fatal(err)
	}
	var n int64
	for _, p := range parts {
		rdr, err := file.OpenParquetFile(p, false)
		if err != nil {
			t.Fatal(err)
		}
		fr, err := pqarrow.NewFileReader(rdr, pqarrow.ArrowReadProperties{}, memory.DefaultAllocator)
		if err != nil {
			t.Fatal(err)
		}
		tbl, err := fr.ReadTable(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range tbl.Column(4).Data().Chunks() {
			sa := c.(*array.String)
			for i := range sa.Len() {
				if sa.Value(i) == kind {
					n++
				}
			}
		}
		tbl.Release()
		_ = rdr.Close()
	}
	return n
}
