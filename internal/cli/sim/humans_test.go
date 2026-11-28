package sim

import (
	"context"
	"strings"
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

	got, ok, err := humansLine(ctx, engine.Summary{Wallets: []int{1, 1, 2, 7}}, &mem)
	want := "humans: 4 owning 11 wallets (2.75 each; by count 1=2 2=1 5+=1); transfers 2\n"
	if err != nil || got != want || !ok {
		t.Fatalf("humansLine = %q, %v, %v\nwant %q", got, ok, err, want)
	}

	if got, ok, _ := humansLine(ctx, engine.Summary{}, &mem); got != "" || !ok {
		t.Fatalf("a run without humans prints %q", got)
	}
}

// The household line checks the fiat the humans hold now against their
// start, pay, bills and trades, and says when it does not add up.
func TestHumansLineFiat(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	var mem eventlog.MemLog
	for _, r := range []struct {
		kind string
		body any
	}{
		{engine.KindIncome, engine.Income{Human: 1, Cents: 1000, Employed: true}},
		{engine.KindExpense, engine.Expense{Human: 1, Due: 900, Paid: 700, Short: 200}},
		{engine.KindTraded, engine.Traded{Side: "buy", Human: 1, Cents: 50}},
		{engine.KindTraded, engine.Traded{Side: "sell", Human: 1, Cents: 30}},
		{engine.KindInvested, engine.Invested{Human: 1, Cents: 50}},
	} {
		body, err := eventlog.Encode(r.body)
		if err != nil {
			t.Fatal(err)
		}

		if err := mem.Append(ctx, eventlog.Record{Class: eventlog.Decision, Kind: r.kind, Body: body}); err != nil {
			t.Fatal(err)
		}
	}

	s := engine.Summary{Wallets: []int{1}, Fiat0: 100, Fiat: 100 + 1000 - 700 - 50 + 30}
	line, ok, err := humansLine(ctx, s, &mem)
	if err != nil || !ok || !strings.Contains(line, "1 paydays (0 out of work, 1 short of bills); 1 purchases") {
		t.Fatalf("line %q ok %v err %v", line, ok, err)
	}

	s.Fiat++
	if _, ok, _ := humansLine(ctx, s, &mem); ok {
		t.Fatal("a cent made from nothing passed the fiat check")
	}
}
