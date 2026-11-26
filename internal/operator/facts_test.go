package operator

import (
	"testing"
	"time"
)

const shelleyJSON = `{"activeSlotsCoeff":0.05,"epochLength":432,"securityParam":12,"slotLength":0.2,
"systemStart":"2026-11-26T10:00:00Z","staking":{"pools":{"aa":{},"bb":{},"cc":{},"dd":{}},"stake":{}}}`

func TestFactsOfGenesis(t *testing.T) {
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
