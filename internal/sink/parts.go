package sink

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/apache/arrow-go/v18/parquet"
	"github.com/apache/arrow-go/v18/parquet/compress"
	"github.com/apache/arrow-go/v18/parquet/pqarrow"
)

const tmpPrefix = ".tmp-"

// partName is the file name of part n, as mithril2pq names them.
func partName(n int) string { return fmt.Sprintf("part-%06d.parquet", n) }

// partNumber parses a part file name, returning -1 if it is not one.
func partNumber(name string) int {
	s, ok := strings.CutPrefix(name, "part-")
	if !ok {
		return -1
	}
	s, ok = strings.CutSuffix(s, ".parquet")
	if !ok {
		return -1
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return -1
	}
	return n
}

// writerProps keeps to features pyarrow, fastparquet and DuckDB all read, the
// set mithril2pq uses: format 2.6 logical types, version 1 data pages,
// dictionary and plain encodings, statistics, no page index, bloom filters or
// byte-stream split. The Arrow schema is stored so pyarrow restores exact
// types.
func writerProps(mem memory.Allocator) (*parquet.WriterProperties, pqarrow.ArrowWriterProperties) {
	props := parquet.NewWriterProperties(
		parquet.WithVersion(parquet.V2_6),
		parquet.WithDataPageVersion(parquet.DataPageV1),
		parquet.WithCompression(compress.Codecs.Zstd),
		parquet.WithDictionaryDefault(true),
		parquet.WithStats(true),
		parquet.WithPageIndexEnabled(false),
		parquet.WithCreatedBy("continuum"),
		parquet.WithAllocator(mem),
	)
	return props, pqarrow.NewArrowWriterProperties(pqarrow.WithStoreSchema(), pqarrow.WithAllocator(mem))
}

// noClose hides Close: the Parquet writer closes its sink, but the file must be
// synced after the footer is written and before it is closed.
type noClose struct{ io.Writer }

// part is one new part file under construction: written under a temporary
// name and renamed into place by commit.
type part struct {
	tmp, final string
	f          *os.File
	w          *pqarrow.FileWriter
	rows       int64
}

func openPart(dir string, n int, s *arrow.Schema, mem memory.Allocator) (*part, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	name := partName(n)
	p := &part{tmp: filepath.Join(dir, tmpPrefix+name), final: filepath.Join(dir, name)}
	f, err := os.Create(p.tmp)
	if err != nil {
		return nil, err
	}
	p.f = f
	props, aprops := writerProps(mem)
	if p.w, err = pqarrow.NewFileWriter(s, noClose{f}, props, aprops); err != nil {
		_ = f.Close()
		_ = os.Remove(p.tmp)
		return nil, fmt.Errorf("start %s: %w", p.tmp, err)
	}
	return p, nil
}

// write appends a batch as one row group.
func (p *part) write(rec arrow.RecordBatch) error {
	defer rec.Release()
	if rec.NumRows() == 0 {
		return nil
	}
	p.rows += rec.NumRows()
	return p.w.Write(rec)
}

// finish completes the file's footer and syncs it, leaving it under its
// temporary name.
func (p *part) finish() error {
	if err := p.w.Close(); err != nil {
		_ = p.f.Close()
		return err
	}
	if err := p.f.Sync(); err != nil {
		_ = p.f.Close()
		return err
	}
	return p.f.Close()
}

// abort drops the file.
func (p *part) abort() {
	_ = p.w.Close()
	_ = p.f.Close()
	_ = os.Remove(p.tmp)
}

func (p *part) commit() error { return os.Rename(p.tmp, p.final) }
