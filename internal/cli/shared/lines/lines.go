// Package lines prints a command's report.
package lines

import (
	"fmt"
	"io"
)

// Writer keeps the first write error so a run of prints checks it once.
type Writer struct {
	Out io.Writer
	Err error
}

// Printf formats to Out unless an earlier print failed.
func (w *Writer) Printf(format string, a ...any) {
	if w.Err == nil {
		_, w.Err = fmt.Fprintf(w.Out, format, a...)
	}
}
