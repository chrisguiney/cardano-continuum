package intent

import (
	"math/rand/v2"
	"testing"

	"github.com/chrisguiney/cardano-continuum/internal/agenda"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/wire"
	"github.com/chrisguiney/cardano-continuum/internal/fakechain"
	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/submit"
)

func TestParseProposal(t *testing.T) {
	t.Parallel()
	hash := "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"
	tests := []struct {
		name string
		id   agenda.ProposalID
		want wire.Ref
		bad  bool
	}{
		{"ok", agenda.ProposalID(hash + "#3"), wire.Ref{Tx: wire.TxID{0x00, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88, 0x99, 0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff, 0x00, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88, 0x99, 0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff}, Index: 3}, false},
		{"no index", agenda.ProposalID(hash), wire.Ref{}, true},
		{"short hash", "0011#1", wire.Ref{}, true},
		{"not hex", agenda.ProposalID("zz" + hash[2:] + "#1"), wire.Ref{}, true},
		{"bad index", agenda.ProposalID(hash + "#x"), wire.Ref{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseProposal(tt.id)
			if (err != nil) != tt.bad || got != tt.want {
				t.Fatalf("ParseProposal(%q) = %v, %v; want %v, bad=%v", tt.id, got, err, tt.want, tt.bad)
			}
		})
	}
}

func TestPaymentSizeNeverBelowFloor(t *testing.T) {
	t.Parallel()
	r := rand.New(rand.NewPCG(1, 2))
	for _, bal := range []uint64{0, 1, 2_000_000, FeeHeadroom + 500, 50_000_000, 1 << 40} {
		got := paymentSize(nil, bal, 1_000_000, r)
		if got < 1_000_000 {
			t.Fatalf("paymentSize(nil, %d) = %d, below the floor", bal, got)
		}
	}
}

func TestAgain(t *testing.T) {
	t.Parallel()
	want := map[submit.Class]bool{submit.Class(fakechain.ExtraRedeemers): true, submit.MissingScriptWitnesses: true}
	for _, c := range Again(UnlockKind) {
		delete(want, c)
	}
	if len(want) != 0 {
		t.Fatalf("Again(unlock) lacks %v", want)
	}
	if got := Again("no such kind"); got != nil {
		t.Fatalf("Again of an unknown kind = %v, want nil", got)
	}
	if len(AllAgain()) < len(Again(UnlockKind)) {
		t.Fatal("AllAgain misses a kind")
	}
}

func TestContractsStartWithTheGenesisOne(t *testing.T) {
	t.Parallel()
	in := New(Spec{}, Env{})
	if in.Count() != 1 {
		t.Fatalf("Count = %d, want 1", in.Count())
	}
	if got := in.pick(rand.New(rand.NewPCG(1, 2))); got != 0 {
		t.Fatalf("pick with one contract = %d, want 0", got)
	}
	if s := in.Save(); s.N != 1 || s.At != population.Never || s.Drawn != 0 {
		t.Fatalf("Saved = %+v", s)
	}
	if err := in.Load(State{At: population.Never}); err != nil || in.Count() != 1 {
		t.Fatalf("Load of an empty state: %v, count %d", err, in.Count())
	}
}
