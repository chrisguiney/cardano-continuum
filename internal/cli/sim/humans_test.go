package sim

import (
	"context"
	"testing"

	"github.com/chrisguiney/cardano-continuum/internal/engine"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
)

func TestHumansLine(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	var mem eventlog.MemLog
	for i, kind := range []string{"transfer", "", "buy_ada", "transfer"} {
		body, err := eventlog.Encode(engine.Submitted{Intent: uint64(i + 1), Kind: kind})
		if err != nil {
			t.Fatal(err)
		}

		rec := eventlog.Record{Seq: uint64(i + 1), Class: eventlog.Decision, Kind: engine.KindSubmitted, Body: body}
		if err := mem.Append(ctx, rec); err != nil {
			t.Fatal(err)
		}
	}

	got, err := humansLine(ctx, []int{1, 1, 2, 7}, &mem)
	want := "humans: 4 owning 11 wallets (2.75 each; by count 1=2 2=1 5+=1); transfers 2\n"
	if err != nil || got != want {
		t.Fatalf("humansLine = %q, %v\nwant %q", got, err, want)
	}

	if got, _ := humansLine(ctx, nil, &mem); got != "" {
		t.Fatalf("a run without humans prints %q", got)
	}
}
