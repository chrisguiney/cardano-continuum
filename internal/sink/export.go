package sink

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/apache/arrow-go/v18/arrow/memory"

	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
)

// Fault stages, passed to Options.Fault in the order they occur. A non-nil
// return aborts the export at that point, as a crash would.
const (
	// StagePartsRenamed: every new part file is in place, the manifest is
	// not yet replaced.
	StagePartsRenamed = "parts-renamed"
	// StageManifestTmp: the new manifest is written and synced under its
	// temporary name, not yet renamed.
	StageManifestTmp = "manifest-tmp"
)

// batchRows is how many rows a table buffers before they are written as a
// row group, bounding memory on a long log.
const batchRows = 64 << 10

// Options configures Export.
type Options struct {
	// Fault, when set, is called at each Stage; its error aborts the export.
	Fault func(stage string) error
}

// Stats describes one export.
type Stats struct {
	// FromSeq is the first seq the export considered new; LastSeq is the
	// last seq in the log.
	FromSeq, LastSeq uint64
	// Rows is the rows written per table; tables with none are absent.
	Rows map[string]int64
}

// Export writes the records of the log at logPath that are newer than the
// directory's manifest as new part files, then replaces the manifest. The log
// is read, never opened for writing, so a live engine may still be appending:
// a torn final frame is ignored.
//
// The whole log is read each time because a SubmitResult names only an
// intent; the attempt and hash it belongs to come from earlier records.
func Export(ctx context.Context, logPath, dir string, opts Options) (Stats, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return Stats{}, err
	}
	m, err := readManifest(dir)
	if err != nil {
		return Stats{}, err
	}
	if err := cleanOrphans(dir, m); err != nil {
		return Stats{}, fmt.Errorf("clean %s: %w", dir, err)
	}
	e := newJob(dir, m, memory.DefaultAllocator)
	defer e.close()
	last, err := e.scan(ctx, logPath)
	if err != nil {
		return Stats{}, fmt.Errorf("export %s: %w", logPath, err)
	}
	st := Stats{FromSeq: m.LastSeq + 1, LastSeq: last, Rows: map[string]int64{}}
	if last < m.LastSeq {
		return Stats{}, fmt.Errorf("export %s: log ends at seq %d, below the exported %d", logPath, last, m.LastSeq)
	}
	if err := e.checkHeader(&m); err != nil {
		return Stats{}, fmt.Errorf("export %s: %w", logPath, err)
	}
	if last == m.LastSeq {
		return st, nil
	}
	if err := e.commit(m, last, opts, st.Rows); err != nil {
		return Stats{}, fmt.Errorf("export %s: %w", logPath, err)
	}
	return st, nil
}

// job is one run's state: the row builder and the parts it is writing.
type job struct {
	dir   string
	b     *builder
	mem   memory.Allocator
	parts map[string]*part
	next  map[string]int
}

func newJob(dir string, m Manifest, mem memory.Allocator) *job {
	e := &job{dir: dir, b: newBuilder(m.LastSeq, mem), mem: mem, parts: map[string]*part{}, next: map[string]int{}}
	for name, files := range m.Tables {
		for _, f := range files {
			e.next[name] = max(e.next[name], partNumber(f.Name)+1)
		}
	}
	return e
}

func (e *job) close() {
	for _, p := range e.parts {
		p.abort()
	}
	e.b.release()
}

// scan reads the log, feeding the builder and flushing full batches. It
// returns the last seq read.
func (e *job) scan(ctx context.Context, logPath string) (uint64, error) {
	var last uint64
	err := scanFile(ctx, logPath, func(r eventlog.Record) error {
		if r.Seq <= last {
			return fmt.Errorf("%w: seq %d after %d", eventlog.ErrSeqGap, r.Seq, last)
		}
		last = r.Seq
		if err := e.b.add(r); err != nil {
			return err
		}
		return e.flush(false)
	})
	return last, err
}

func (e *job) flush(all bool) error {
	for _, name := range TableNames {
		t := e.b.tables[name]
		if t.rows == 0 || (!all && t.rows < batchRows) {
			continue
		}
		p := e.parts[name]
		if p == nil {
			var err error
			p, err = openPart(filepath.Join(e.dir, name), e.next[name], schemas[name], e.mem)
			if err != nil {
				return fmt.Errorf("table %s: %w", name, err)
			}
			e.parts[name] = p
		}
		t.rows = 0
		if err := p.write(t.take()); err != nil {
			return fmt.Errorf("table %s: %w", name, err)
		}
	}
	return nil
}

// commit finishes the parts, renames them into place and replaces the
// manifest. Until the manifest rename the old manifest is the truth.
func (e *job) commit(m Manifest, last uint64, opts Options, rows map[string]int64) error {
	if err := e.flush(true); err != nil {
		return err
	}
	for name, p := range e.parts {
		if err := p.finish(); err != nil {
			return fmt.Errorf("table %s: %w", name, err)
		}
	}
	nm := Manifest{SchemaVersion: SchemaVersion, LastSeq: last, Header: m.Header, Tables: map[string][]File{}}
	for name, files := range m.Tables {
		nm.Tables[name] = append([]File(nil), files...)
	}
	for name, p := range e.parts {
		if err := p.commit(); err != nil {
			return fmt.Errorf("table %s: %w", name, err)
		}
		rows[name] = p.rows
		nm.Tables[name] = append(nm.Tables[name], File{Name: partName(e.next[name]), Rows: p.rows, FirstSeq: m.LastSeq + 1, LastSeq: last})
	}
	e.parts = map[string]*part{}
	if err := syncTables(e.dir); err != nil {
		return err
	}
	if err := hook(opts.Fault, StagePartsRenamed); err != nil {
		return err
	}
	return writeManifest(e.dir, nm, opts.Fault)
}

func syncTables(dir string) error {
	for _, n := range TableNames {
		if err := syncDir(filepath.Join(dir, n)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

var errNoHeader = errors.New("log has no header record")

// checkHeader ties the directory to one run: the first export records the
// header's fingerprints, later ones must see the same.
func (e *job) checkHeader(m *Manifest) error {
	h := e.b.header
	if h == nil {
		if m.Header == "" {
			return errNoHeader
		}
		return nil
	}
	fp := fmt.Sprintf("%x/%x/%x", h.SeedFingerprint, h.ProfileHash, h.GenesisHash)
	if m.Header != "" && m.Header != fp {
		return fmt.Errorf("%w: directory holds run %s, log is run %s", eventlog.ErrHeaderMismatch, m.Header, fp)
	}
	m.Header = fp
	return nil
}

// scanFile calls fn for each complete record of the log file, read-only. A
// torn final frame is the writer's unfinished append, not an error; a bad
// frame followed by more data is.
func scanFile(ctx context.Context, path string, fn func(eventlog.Record) error) (err error) {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	rd := bufio.NewReaderSize(f, 1<<20)
	head := make([]byte, 8)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := io.ReadFull(rd, head); err != nil {
			return nil // clean end, or a torn frame header
		}
		n, err := eventlog.FrameBodyLen(head)
		if err != nil {
			return err
		}
		buf := make([]byte, len(head)+n)
		copy(buf, head)
		if _, err := io.ReadFull(rd, buf[len(head):]); err != nil {
			return nil // torn body
		}
		rec, _, err := eventlog.DecodeFrame(buf)
		if err != nil {
			if _, perr := rd.Peek(1); errors.Is(err, eventlog.ErrCorrupt) && perr != nil {
				return nil // torn final frame
			}
			return err
		}
		if err := fn(rec); err != nil {
			return err
		}
	}
}
