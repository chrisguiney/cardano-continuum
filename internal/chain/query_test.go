package chain

import (
	"errors"
	"testing"
	"time"
)

func TestGenesisGuard(t *testing.T) {
	want := Genesis{
		Magic: 42, Start: time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC),
		EpochLength: 200, SecurityParam: 10, SlotLength: time.Second,
	}
	tests := []struct {
		name string
		mod  func(*Genesis)
		err  error
	}{
		{"match", func(*Genesis) {}, nil},
		{"other start", func(g *Genesis) { g.Start = g.Start.Add(time.Second) }, ErrGenesisMismatch},
		{"other k", func(g *Genesis) { g.SecurityParam = 5 }, ErrGenesisMismatch},
		{"other epoch length", func(g *Genesis) { g.EpochLength = 100 }, ErrGenesisMismatch},
		{"other slot length", func(g *Genesis) { g.SlotLength = 2 * time.Second }, ErrGenesisMismatch},
		{"other magic", func(g *Genesis) { g.Magic = 43 }, ErrGenesisMismatch},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := want
			tc.mod(&got)
			if err := Guard(want, got); !errors.Is(err, tc.err) {
				t.Errorf("Guard = %v, want %v", err, tc.err)
			}
		})
	}
	for _, magic := range []uint32{764824073, 1, 2, 4} {
		g := want
		g.Magic = magic
		if err := Guard(g, g); !errors.Is(err, ErrPublicNetwork) {
			t.Errorf("magic %d: Guard = %v, want ErrPublicNetwork", magic, err)
		}
	}
}
