package export

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"

	"github.com/chrisguiney/cardano-continuum/internal/sink"
)

// Run writes an event log as Parquet tables. Re-running it on a longer
// log appends only the new records.
func Run(ctx context.Context, args []string, out io.Writer) error {
	errArgs := errors.New("usage: continuum export <events.log> --out <dir>")
	// The log path may come before or after the flags; flag.Parse stops at
	// the first non-flag, so take a leading path off first.
	var logPath string
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		logPath, args = args[0], args[1:]
	}
	fs := flag.NewFlagSet("export", flag.ContinueOnError)
	dir := fs.String("out", "", "directory for the Parquet tables and manifest")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if logPath == "" && fs.NArg() == 1 {
		logPath = fs.Arg(0)
	} else if fs.NArg() != 0 {
		return errArgs
	}
	if logPath == "" || *dir == "" {
		return errArgs
	}
	st, err := sink.Export(ctx, logPath, *dir, sink.Options{})
	if err != nil {
		return err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "exported seq %d..%d to %s\n", st.FromSeq, st.LastSeq, *dir)
	for _, name := range slices.Sorted(maps.Keys(st.Rows)) {
		fmt.Fprintf(&b, "  %-17s %d rows\n", name, st.Rows[name])
	}
	_, err = io.WriteString(out, b.String())
	return err
}
