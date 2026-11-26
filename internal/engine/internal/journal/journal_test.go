package journal

import (
	"testing"

	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/wire"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
)

func TestJournalBuffersOneInput(t *testing.T) {
	var j Journal
	j.Begin(4)
	j.Emit(10, wire.KindSpawned, 3, wire.Spawned{Profile: "p"})
	j.Append(eventlog.Record{Kind: "x"})
	j.End(7, wire.TxID{1}, true)
	if j.Seq() != 4 || len(j.Ends()) != 1 || j.Ends()[0].Seq != 4 || !j.Ends()[0].Released {
		t.Fatalf("seq %d ends %+v", j.Seq(), j.Ends())
	}
	out := j.Take()
	if len(out) != 2 || out[0].Kind != wire.KindSpawned || out[0].At != 10 || out[0].Actor != 3 || out[1].Kind != "x" {
		t.Fatalf("records %+v", out)
	}
	if again := j.Take(); len(again) != 0 {
		t.Fatalf("Take did not empty the buffer: %+v", again)
	}
}

func TestBeginAndDropClear(t *testing.T) {
	var j Journal
	j.Emit(1, wire.KindSpawned, 1, wire.Spawned{})
	j.Drop()
	if len(j.Take()) != 0 {
		t.Fatal("Drop kept records")
	}
	j.End(1, wire.TxID{}, false)
	j.Begin(9)
	if len(j.Ends()) != 0 || j.Seq() != 9 {
		t.Fatalf("Begin kept ends %v seq %d", j.Ends(), j.Seq())
	}
}
