package phase

import (
	"testing"
	"time"

	"github.com/chrisguiney/cardano-continuum/api/v1alpha1"
	"github.com/chrisguiney/cardano-continuum/internal/chain"
)

const shelleyJSON = `{"activeSlotsCoeff":0.05,"epochLength":432,"securityParam":12,"slotLength":0.2,
"systemStart":"2026-11-26T10:00:00Z","staking":{"pools":{"aa":{},"bb":{},"cc":{},"dd":{}},"stake":{}}}`

func TestFactsOfGenesis(t *testing.T) {
	t.Parallel()
	f, err := FactsOf([]byte(shelleyJSON))
	if err != nil {
		t.Fatal(err)
	}
	if !f.Start.Equal(time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC)) || f.SlotLen != 200*time.Millisecond ||
		f.F != 0.05 || f.Epoch != 432 || f.K != 12 || f.Share != 0.25 {
		t.Fatalf("facts %+v", f)
	}
	if got := f.SlotAt(f.Start.Add(3 * time.Second)); got != 15 {
		t.Fatalf("slot %d, want 15", got)
	}
}

func TestFactsOfRejects(t *testing.T) {
	t.Parallel()
	for name, doc := range map[string]string{
		"not json":       `{`,
		"no start":       `{"slotLength":1}`,
		"bad start":      `{"slotLength":1,"systemStart":"yesterday"}`,
		"no slot length": `{"systemStart":"2026-11-26T10:00:00Z"}`,
	} {
		if _, err := FactsOf([]byte(doc)); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

func TestWithPriorTip(t *testing.T) {
	t.Parallel()
	prior := v1alpha1.TipStatus{Slot: 74, Height: 6}
	if got := withPriorTip(Seen{}, prior); !got.HaveTip || got.Tip.Slot != 74 || got.Tip.Height != 6 {
		t.Errorf("empty window: %+v", got)
	}
	seen := Seen{HaveTip: true, Tip: chain.Mark{Point: chain.Point{Slot: 90}}}
	if got := withPriorTip(seen, prior); got.Tip.Slot != 90 {
		t.Errorf("a seen tip is replaced: %+v", got)
	}
	if got := withPriorTip(Seen{}, v1alpha1.TipStatus{}); got.HaveTip {
		t.Errorf("no prior tip invented one: %+v", got)
	}
}
