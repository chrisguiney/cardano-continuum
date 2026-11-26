package oracle

import (
	"strconv"
	"testing"

	"github.com/chrisguiney/cardano-continuum/internal/chain"
	"github.com/chrisguiney/cardano-continuum/internal/fakechain"
	"github.com/chrisguiney/cardano-continuum/internal/mistake"
	"github.com/chrisguiney/cardano-continuum/internal/submit"
)

func branchOf(pool string, c *fakechain.Chain) Branch {
	var pts []chain.Point
	for _, b := range c.Blocks() {
		pts = append(pts, chain.Point{Slot: b.Slot, Hash: chain.TxID(b.Hash)})
	}
	return Branch{Node: Node{Impl: "fakechain", Pool: pool}, Points: pts}
}

func tipsFindings(t *testing.T, a, b *fakechain.Chain) []Finding {
	t.Helper()
	return check(t, "tips", Snapshot{Tips: []Branch{branchOf("a", a), branchOf("b", b)}})
}

// Two fake chains from one genesis agree block for block until one is handed
// a transaction the other never sees. The fork then deepens on both sides;
// TipsAgree is quiet while either side could still roll back to the other
// and fires once both sides hold more than k blocks past the fork point.
func TestTipsAgreeOnFakeChainFork(t *testing.T) {
	wd := newWorld(t)
	other := newChain(t, wd.w.Address())
	step := func(slot uint64) {
		t.Helper()
		for _, c := range []*fakechain.Chain{wd.c, other} {
			if err := c.AdvanceTo(slot); err != nil {
				t.Fatal(err)
			}
		}
	}
	step(6)
	if fs := tipsFindings(t, wd.c, other); len(fs) != 0 {
		t.Fatalf("equal chains: %+v", fs)
	}
	forkSlot := wd.c.Blocks()[len(wd.c.Blocks())-1].Slot

	pay := wd.base(t, mistake.DoubleSubmit)
	if o := wd.send(pay.Tx); o.Verdict != submit.Accepted {
		t.Fatalf("pay: %+v", o)
	}
	step(6 + 2*k)
	if fs := tipsFindings(t, wd.c, other); len(fs) != 0 {
		t.Fatalf("a fork k blocks deep fired: %+v", fs)
	}
	if wd.c.Immutable() == 0 {
		t.Fatal("the fake chain has no immutable prefix yet")
	}
	step(6 + 2*(k+1))
	fs := tipsFindings(t, wd.c, other)
	if len(fs) != 1 || fs[0].Kind != DeepFork {
		t.Fatalf("fork deeper than k: findings %+v", fs)
	}
	if !hasFact(fs[0], "fork_slot", strconv.FormatUint(forkSlot, 10)) {
		t.Fatalf("fork point %d not recorded: %+v", forkSlot, fs[0].Facts)
	}
}
