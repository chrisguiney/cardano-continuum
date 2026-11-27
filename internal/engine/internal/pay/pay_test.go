package pay

import (
	"slices"
	"testing"

	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/wire"
	"github.com/chrisguiney/cardano-continuum/internal/mistake"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

func TestDeposit(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		held         uint64
		d            int64
		want, wantIn uint64
	}{
		{"paid", 0, 5, 0, 5},
		{"refunded from the pot", 7, -5, 0, 2},
		{"refund of a genesis deposit", 3, -5, 2, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pa := New()
			pa.deposits = tt.held
			if got := pa.Deposit(tt.d); got != tt.want {
				t.Fatalf("Deposit(%d) = %d, want %d", tt.d, got, tt.want)
			}
			if held := pa.deposits; held != tt.wantIn {
				t.Fatalf("deposits %d, want %d", held, tt.wantIn)
			}
		})
	}
}

func TestRefusable(t *testing.T) {
	t.Parallel()
	tests := []struct {
		v      wire.Verdict
		resend bool
		want   bool
	}{
		{wire.VerdictRejected, false, true},
		{wire.VerdictRejected, true, false},
		{wire.VerdictUnreached, false, false},
		{wire.VerdictAccepted, false, false},
	}
	for _, tt := range tests {
		if got := Refusable(tt.v, tt.resend); got != tt.want {
			t.Errorf("Refusable(%d, %v) = %v, want %v", tt.v, tt.resend, got, tt.want)
		}
	}
}

// TestInFlightOrder: a front-run goes first, then intents by ID; one still
// backing off waits. Intents are added in ID order, as the engine adds them.
func TestInFlightOrder(t *testing.T) {
	t.Parallel()
	pa := New()
	for _, p := range []*wire.Pending{
		{ID: 1, Attempt: wire.Attempt{Tx: wire.TxID{1}, Txs: []wire.TxID{{1}}, Awaiting: true, NotBefore: 50}},
		{ID: 2, Attempt: wire.Attempt{Tx: wire.TxID{2}, Txs: []wire.TxID{{2}}}, Strike: wire.Strike{Jump: true}},
		{ID: 3, Attempt: wire.Attempt{Tx: wire.TxID{3}, Txs: []wire.TxID{{3}}, Awaiting: true}},
		{ID: 4, Attempt: wire.Attempt{Tx: wire.TxID{4}, Txs: []wire.TxID{{4}}, Awaiting: true}, Faults: wire.Faults{Carried: mistake.Of(mistake.DoubleSubmit)}},
	} {
		pa.Add(p)
	}
	pa.await(pa.Get(2))
	ids := func(now sched.Time) []uint64 {
		var out []uint64
		for _, s := range pa.InFlight(now) {
			out = append(out, s.Intent)
		}
		return out
	}
	if got := ids(10); !slices.Equal(got, []uint64{2, 3, 4}) {
		t.Fatalf("in flight at 10: %v, want [2 3 4]", got)
	}
	if got := ids(50); !slices.Equal(got, []uint64{2, 1, 3, 4}) {
		t.Fatalf("in flight at 50: %v, want [2 1 3 4]", got)
	}
	if s := pa.InFlight(50); !s[3].Twice {
		t.Fatal("a DoubleSubmit is not sent twice")
	}
	pa.Unawait(3)
	pa.drop(4)
	if got := ids(50); !slices.Equal(got, []uint64{2, 1}) {
		t.Fatalf("in flight after unawait and drop: %v, want [2 1]", got)
	}
	if _, ok := pa.Of(wire.TxID{4}); ok || pa.Len() != 3 {
		t.Fatalf("dropped intent still indexed: len %d", pa.Len())
	}
}
