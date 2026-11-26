//go:build duckdb

package sink

import (
	"context"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// pyCounts prints "<table> <count>" for each table, read by DuckDB and then
// by pyarrow, so both readers must accept the files.
const pyCounts = `
import sys, duckdb, pyarrow.dataset as ds
d = sys.argv[1]
con = duckdb.connect()
for t in sys.argv[2:]:
    n = con.execute("SELECT count(*) FROM read_parquet(?)", [d + "/" + t + "/part-*.parquet"]).fetchone()[0]
    m = ds.dataset(d + "/" + t, format="parquet").count_rows()
    assert n == m, (t, n, m)
    print(t, n)
`

// TestDuckDBReadsTables checks row counts through DuckDB and pyarrow. It
// skips when no python3 with both modules is installed.
func TestDuckDBReadsTables(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("no python3")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := exec.CommandContext(ctx, py, "-I", "-c", "import duckdb, pyarrow.dataset").Run(); err != nil {
		t.Skip("python3 lacks duckdb or pyarrow")
	}
	d := t.TempDir()
	lp := filepath.Join(d, "events.log")
	writeLog(t, lp, fixture(t))
	out := filepath.Join(d, "out")
	export(t, lp, out, Options{})
	args := append([]string{"-I", "-c", pyCounts, out}, TableNames...)
	b, err := exec.CommandContext(ctx, py, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, b)
	}
	got := map[string]string{}
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		f := strings.Fields(l)
		got[f[0]] = f[1]
	}
	for _, name := range TableNames {
		if want := strconv.Itoa(len(dump(t, out, name))); got[name] != want {
			t.Errorf("%s: duckdb %s rows, go reader %s", name, got[name], want)
		}
	}
}
