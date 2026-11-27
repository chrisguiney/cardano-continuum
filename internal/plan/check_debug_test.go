//go:build debug

package plan

import (
	"strings"
	"testing"
)

// mustPanic runs step and fails unless it panics with a plan check.
func mustPanic(t *testing.T, step func()) {
	t.Helper()
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("a corrupt state was adopted without a check failing")
		}

		if s, ok := r.(string); !ok || !strings.Contains(s, "plan: ") {
			panic(r)
		}
	}()
	step()
}

// The owner index is checked when a step adopts a state, so a corrupt
// index fails on the next step whether or not it is committed.
func TestStepChecksOwnerIndex(t *testing.T) {
	for _, tc := range []struct {
		name string
		step func(w *world)
	}{
		{"volatile block", func(w *world) { w.block(10) }},
		{"committed reserve", func(w *world) { w.reserve(3, w.attempt(3, 2, farTTL, g3)) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t)
			w.l.st.out.byOwner[1][ref(999)] = struct{}{}
			mustPanic(t, func() { tc.step(w) })
		})
	}
}

// The spent cache must match the blocks it was built from when a state
// with it is adopted.
func TestStepChecksSpentCache(t *testing.T) {
	w := newWorld(t)
	w.block(10)
	w.l.Available(1, 1, Opts{})
	w.l.st.view.spent[ref(999)] = struct{}{}
	mustPanic(t, func() { w.reserve(3, w.attempt(3, 2, farTTL, g3)) })
}
