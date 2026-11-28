package poolwatch

import (
	"testing"

	"github.com/chrisguiney/cardano-continuum/internal/chain"
)

func mk(h uint64, tag byte) chain.Mark {
	var m chain.Mark
	m.Height, m.Slot = h, h*10
	m.Hash[0], m.Hash[1] = tag, byte(h)
	return m
}

func heights(w *Window) []uint64 {
	var out []uint64
	for _, m := range w.Marks() {
		out = append(out, m.Height)
	}
	return out
}

func TestWindowForwardCapsAtSize(t *testing.T) {
	t.Parallel()
	w := NewWindow(3)
	for h := uint64(1); h <= 5; h++ {
		w.Forward(mk(h, 'a'))
	}
	if got := heights(w); len(got) != 3 || got[0] != 3 || got[2] != 5 {
		t.Fatalf("heights %v", got)
	}
}

func TestWindowBackwardPopsToPoint(t *testing.T) {
	t.Parallel()
	w := NewWindow(8)
	for h := uint64(1); h <= 5; h++ {
		w.Forward(mk(h, 'a'))
	}
	w.Backward(mk(3, 'a').Point)
	if got := heights(w); len(got) != 3 || got[2] != 3 {
		t.Fatalf("heights %v", got)
	}
	w.Forward(mk(4, 'b'))
	if tip, _ := w.Tip(); tip.Hash[0] != 'b' {
		t.Fatalf("tip %+v", tip)
	}
}

func TestWindowBackwardBeyondWindowResets(t *testing.T) {
	t.Parallel()
	w := NewWindow(8)
	w.Forward(mk(5, 'a'))
	w.Forward(mk(6, 'a'))
	w.Backward(mk(1, 'z').Point)
	if _, ok := w.Tip(); ok {
		t.Fatal("window kept marks after a rollback past it")
	}
}

func TestWindowMarksIsACopy(t *testing.T) {
	t.Parallel()
	w := NewWindow(4)
	w.Forward(mk(1, 'a'))
	w.Marks()[0].Height = 99
	if got := heights(w); got[0] != 1 {
		t.Fatalf("aliased %v", got)
	}
}

func TestWindowBackwardMeasuresDepth(t *testing.T) {
	t.Parallel()
	w := NewWindow(8)
	for h := uint64(1); h <= 5; h++ {
		w.Forward(mk(h, 'a'))
	}
	rb, ok := w.Backward(mk(3, 'a').Point)
	if !ok {
		t.Fatal("a rollback two blocks deep was not reported")
	}
	if rb.Blocks != 2 || rb.Slots != 20 || rb.From.Height != 5 || rb.To.Slot != 30 || rb.Beyond {
		t.Fatalf("rollback %+v", rb)
	}
}

// The first RollBackward after an intersect names the tip itself; it is the
// protocol's handshake, not a rollback.
func TestWindowBackwardToTipIsNotARollback(t *testing.T) {
	t.Parallel()
	w := NewWindow(8)
	w.Forward(mk(5, 'a'))
	if _, ok := w.Backward(mk(5, 'a').Point); ok {
		t.Fatal("a rollback to the tip counted")
	}
	if got := heights(w); len(got) != 1 {
		t.Fatalf("heights %v", got)
	}
}

func TestWindowBackwardPastWindowIsALowerBound(t *testing.T) {
	t.Parallel()
	w := NewWindow(8)
	w.Forward(mk(5, 'a'))
	w.Forward(mk(6, 'a'))
	rb, ok := w.Backward(mk(1, 'z').Point)
	if !ok || !rb.Beyond || rb.Blocks != 2 || rb.Slots != 50 {
		t.Fatalf("rollback %+v ok=%v", rb, ok)
	}
}

func TestWindowBackwardOnEmptyWindowIsNotACount(t *testing.T) {
	t.Parallel()
	w := NewWindow(8)
	if _, ok := w.Backward(mk(1, 'a').Point); ok {
		t.Fatal("an empty window reported a rollback")
	}
}
