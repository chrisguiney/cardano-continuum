package people

import (
	"testing"

	"github.com/chrisguiney/cardano-continuum/internal/engine"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
	"github.com/chrisguiney/cardano-continuum/internal/store"
)

func rec(t *testing.T, seq uint64, at int64, kind string, body any) eventlog.Record {
	t.Helper()
	b, err := eventlog.Encode(body)
	if err != nil {
		t.Fatal(err)
	}
	return eventlog.Record{Seq: seq, At: sched.Time(at), Class: eventlog.Decision, Kind: kind, Body: b}
}

func fold(t *testing.T) *Fold {
	t.Helper()
	var f Fold
	for _, r := range []eventlog.Record{
		rec(t, 0, 5, engine.KindTraded, engine.Traded{Intent: 6, Side: "sell", Human: 1, Cents: 1}),
		rec(t, 1, 10, engine.KindIncome, engine.Income{Human: 1, Cents: 1000, Currency: "EUR", Employed: true}),
		rec(t, 2, 10, engine.KindExpense, engine.Expense{Human: 1, Due: 900, Paid: 900}),
		rec(t, 3, 20, engine.KindInvested, engine.Invested{Intent: 7, Human: 1, Wallet: 2, Cents: 25, Style: "dca"}),
		rec(t, 4, 30, engine.KindTraded, engine.Traded{Intent: 7, Side: "buy", Human: 1, Cents: 25}),
		rec(t, 5, 40, engine.KindTraded, engine.Traded{Intent: 8, Side: "buy", Human: 1, Cents: 60}),
		rec(t, 6, 50, engine.KindTraded, engine.Traded{Intent: 9, Side: "sell", Human: 1, Cents: 40}),
		rec(t, 7, BinMs+1, engine.KindEmployment, engine.Employment{Human: 1}),
		rec(t, 8, BinMs+1, engine.KindIncome, engine.Income{Human: 1, Cents: 300, Currency: "EUR"}),
		rec(t, 9, BinMs+1, engine.KindExpense, engine.Expense{Human: 1, Due: 900, Paid: 300, Short: 600}),
		rec(t, 10, 60, engine.KindTraded, engine.Traded{Intent: 10, Side: "buy", Cents: 99}),
	} {
		if err := f.Add(r); err != nil {
			t.Fatal(err)
		}
	}
	return &f
}

// The fold tells purchases from income from the agendas' own buys, and
// buckets the flows by currency and five minutes.
func TestFoldFlows(t *testing.T) {
	fl := fold(t).Flows()
	if len(fl) != 2 {
		t.Fatalf("flows %+v, want two buckets", fl)
	}

	first, second := fl[0], fl[1]
	if first.Currency != "EUR" || first.Income != 1000 || first.Bills != 900 || first.Invested != 25 || first.Scored != 60 || first.Sold != 40 {
		t.Fatalf("first bucket %+v", first)
	}

	if second.At != BinMs || second.Paydays != 1 || second.Out != 1 || second.Bills != 300 {
		t.Fatalf("second bucket %+v", second)
	}
}

func wallets() Wallets {
	owner := func(purpose string) *store.Owner {
		return &store.Owner{
			Human: 1, Purpose: purpose, Motivation: "hodl", Tier: "mid", Target: 2, Wallets: []uint64{2, 3},
			Money: &store.Money{Currency: "EUR", Fiat: 500, Held: 25, Income: 1000, Ratio: 0.9, Share: 0.2, Steps: 4, Style: "dca", Buffer: 1},
		}
	}
	idents := map[uint64]store.Identity{
		2: {Owner: owner("spending"), Holdings: store.Holdings{Settled: 7_000_000}},
		3: {Owner: owner("savings"), Holdings: store.Holdings{Settled: 3_000_000}},
		4: {Kind: "pool"},
	}
	return Wallets{Idents: idents, Profiles: map[uint64]string{2: "holder"}, States: map[uint64]string{2: "active"}}
}

func TestBuildAndDetail(t *testing.T) {
	f := fold(t)
	v := Build(wallets(), f)
	if len(v.Humans) != 1 {
		t.Fatalf("humans %+v", v.Humans)
	}

	r := v.Humans[0]
	if r.Wallets != 2 || r.Lovelace != 10_000_000 || r.Bills != 900 || r.Purchases != 1 || r.Invested != 25 || r.Fiat != 500 {
		t.Fatalf("row %+v", r)
	}

	p, ok := Detail(1, wallets(), f)
	if !ok || len(p.WalletList) != 2 || p.WalletList[1].Purpose != "savings" || p.Months != 2 || p.Out != 1 || p.Short != 1 || p.Sold != 41 || p.Bought != 85 {
		t.Fatalf("person %+v", p)
	}

	if len(p.Events) == 0 || p.Events[len(p.Events)-1].Detail != "bills short: a forced sale of ADA follows" {
		t.Fatalf("events %+v", p.Events)
	}

	if _, ok := Detail(9, wallets(), f); ok {
		t.Fatal("a human no wallet names has a page")
	}
}
