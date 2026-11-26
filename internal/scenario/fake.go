package scenario

import (
	"context"
	"fmt"

	"github.com/chrisguiney/cardano-continuum/internal/fakechain"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
	"github.com/chrisguiney/cardano-continuum/internal/submit"
)

// FakeBackend runs each scenario on a fresh in-memory chain built to the
// scenario's preconditions.
type FakeBackend struct{}

// fakeEpoch is long enough that no scenario crosses an epoch boundary, whose
// effects the fake chain does not model.
const fakeEpoch = 1 << 20

// Env implements Backend: the fake has no reward balances and refuses
// phase-2-invalid transactions.
func (FakeBackend) Env() Env {
	return Env{NetworkID: 0, Params: fakechain.DefaultParams()}
}

// Open implements Backend.
func (FakeBackend) Open(_ context.Context, pre Preconditions) (Target, error) {
	slots, err := sched.NewSlots(1000, fakeEpoch)
	if err != nil {
		return nil, err
	}
	params := pre.Params
	c, err := fakechain.New(fakechain.Config{
		Slots: slots, Params: &params, K: 10, BlockEvery: 1,
		Genesis: []fakechain.GenesisOutput{{Address: pre.Funder, Lovelace: pre.Funds}},
	})
	if err != nil {
		return nil, fmt.Errorf("fake chain: %w", err)
	}
	return &fakeTarget{c: c}, nil
}

type fakeTarget struct{ c *fakechain.Chain }

func (*fakeTarget) Name() string { return "fakechain" }

// Slot mirrors the fake's mempool: it validates at the slot after the tip.
func (t *fakeTarget) Slot() uint64 {
	blocks := t.c.Blocks()
	if len(blocks) == 0 {
		return 0
	}
	return blocks[len(blocks)-1].Slot + 1
}

func (t *fakeTarget) Submit(ctx context.Context, tx []byte) submit.Outcome {
	return submit.Send(ctx, submit.NewFake(t.c), fakechain.EraConway, tx)
}

func (t *fakeTarget) AdvanceTo(_ context.Context, slot uint64) error { return t.c.AdvanceTo(slot) }

func (t *fakeTarget) Inject(f fakechain.Fault) error { return t.c.Inject(f) }
