package sink

import (
	"fmt"
	"strings"

	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/oracle"
)

// finding writes one oracle finding row.
func (b *builder) finding(r eventlog.Record) error {
	f, err := oracle.Decode(r)
	if err != nil {
		return err
	}
	if n := len(f.TxHash); n != 0 && n != 32 {
		return fmt.Errorf("finding tx hash: %w: %d", errHashLen, n)
	}
	t := b.tables[tFinding]
	t.u64(0, r.Seq)
	t.i64(1, int64(r.At))
	t.str(2, f.ID)
	t.str(3, string(f.Kind))
	t.str(4, f.Severity.String())
	t.u64(5, f.Seq)
	t.str(6, f.Summary)
	if len(f.TxHash) == 0 {
		t.null(7)
	} else {
		t.hash(7, f.TxHash)
	}
	if len(f.Tx) == 0 {
		t.null(8)
	} else {
		t.bin(8, f.Tx)
	}
	if f.Expected == nil {
		t.null(9)
	} else {
		t.str(9, f.Expected.String())
	}
	optStr(t, 10, verdictsText(f.Verdicts))
	t.str(11, factsText(f.Facts))
	t.flag(12, f.Reproducible)
	t.u64(13, f.Seed)
	optStr(t, 14, f.Replay)
	t.done()
	return nil
}

func optStr(t *table, c int, v string) {
	if v == "" {
		t.null(c)
		return
	}
	t.str(c, v)
}

func verdictsText(vs []oracle.Verdict) string {
	parts := make([]string, len(vs))
	for i, v := range vs {
		cs := make([]string, len(v.Classes))
		for j, c := range v.Classes {
			cs[j] = string(c)
		}
		parts[i] = strings.TrimSpace(fmt.Sprintf("%s@%s=%s %s", v.Node.Impl, v.Node.Pool, v.Verdict, strings.Join(cs, ",")))
	}
	return strings.Join(parts, ";")
}

func factsText(fs []oracle.Fact) string {
	parts := make([]string, len(fs))
	for i, f := range fs {
		parts[i] = f.Name + "=" + f.Value
	}
	return strings.Join(parts, ";")
}
