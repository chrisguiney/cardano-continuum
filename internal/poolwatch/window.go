// Package poolwatch follows each pool's node over node-to-node chain-sync
// and keeps the newest blocks of its chain, so forks between
// implementations show while they happen. It only reads headers: every
// node serves node-to-node, while dingo's node-to-client socket is
// loopback-only inside its pod.
package poolwatch

import (
	"slices"

	"github.com/chrisguiney/cardano-continuum/internal/chain"
)

// Window is the newest blocks of one node's chain, oldest first. It is not
// safe for concurrent use; the Tracker serialises access.
type Window struct {
	size  int
	marks []chain.Mark
}

// NewWindow returns a Window that keeps at most size blocks.
func NewWindow(size int) *Window { return &Window{size: max(size, 1)} }

// Forward appends a block, dropping the oldest at capacity.
func (w *Window) Forward(m chain.Mark) {
	w.marks = append(w.marks, m)
	if len(w.marks) > w.size {
		w.marks = slices.Delete(w.marks, 0, len(w.marks)-w.size)
	}
}

// Backward rolls the window back to p. A point older than the window empties
// it: the blocks it held are gone and the new ones arrive as Forward.
func (w *Window) Backward(p chain.Point) {
	for i := len(w.marks) - 1; i >= 0; i-- {
		if w.marks[i].Point == p {
			w.marks = w.marks[:i+1]
			return
		}
	}
	w.marks = w.marks[:0]
}

// Tip is the newest block, or false when the window is empty.
func (w *Window) Tip() (chain.Mark, bool) {
	if len(w.marks) == 0 {
		return chain.Mark{}, false
	}
	return w.marks[len(w.marks)-1], true
}

// Marks returns a copy of the window, oldest first.
func (w *Window) Marks() []chain.Mark { return slices.Clone(w.marks) }
