package engine

import (
	"bytes"
	"errors"
	"testing"

	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/keys"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

// faucetConfig is testConfig with the population funded by a faucet instead
// of genesis, as on a devnet made by internal/genesis.
func faucetConfig(t testing.TB, seed uint64, wallets int) Config {
	t.Helper()
	cfg := testConfig(t, seed, wallets)
	cfg.Faucet = 50_000_000_000
	cfg.RefillEvery = 8
	return cfg
}

func TestFaucetGenesisIsTheFaucetOnly(t *testing.T) {
	t.Parallel()
	cfg := faucetConfig(t, 21, 5)
	funds, err := cfg.Genesis()
	if err != nil {
		t.Fatal(err)
	}
	ks, err := keys.New(cfg.Seed, cfg.NetworkID)
	if err != nil {
		t.Fatal(err)
	}
	if len(funds) != 1 || funds[0].Lovelace != cfg.Faucet ||
		funds[0].Address.String() != ks.Faucet().Address().String() {
		t.Fatalf("genesis %+v, want only internal/genesis's faucet with %d", funds, cfg.Faucet)
	}
}

func TestFaucetNeedsARefillInterval(t *testing.T) {
	t.Parallel()
	cfg := faucetConfig(t, 21, 5)
	cfg.RefillEvery = 0
	if _, err := New(cfg); !errors.Is(err, ErrConfig) {
		t.Fatalf("New = %v, want ErrConfig", err)
	}
}

// TestFaucetFundsPopulation starts every wallet empty: the faucet's refills
// are the only way they get money, so confirmed payments show the refills
// confirmed, and the supply still adds up to the faucet's genesis output.
func TestFaucetFundsPopulation(t *testing.T) {
	t.Parallel()
	cfg := faucetConfig(t, 22, 6)
	w := newWorld(t, cfg)
	w.run(cfg.Slots.EpochStart(2), 50, nil)
	k := countKinds(t, w.log)
	if k[KindRefill] == 0 || k[KindSubmitted] == 0 || k[KindConfirmed] <= k[KindRefill] {
		t.Fatalf("want refills and confirmed payments: %v", k)
	}
	p, err := w.start(nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	e := p.r.Engine()
	if total := genesisTotal(t, cfg); e.Supply() != total || w.chain.UnspentLovelace()+w.chain.Fees() != total {
		t.Fatalf("supply %d, chain %d, genesis %d", e.Supply(), w.chain.UnspentLovelace()+w.chain.Fees(), total)
	}
	if want := derive(t, cfg, w.chain); !sameBalances(want, e.Balances()) {
		t.Fatalf("engine balances %v, Derive(chain) %v", e.Balances(), want)
	}
	if a := auditLog(t, w.log); a.DoublePaid != 0 || a.Confirmed != k[KindConfirmed] {
		t.Fatalf("audit %+v, kinds %v", a, k)
	}
}

// TestFaucetCrashConverges kills a faucet run at spread-out crash points; the
// recovered run must write the uninterrupted run's log byte for byte, which
// covers the refill schedule and a refill in flight across a restart.
func TestFaucetCrashConverges(t *testing.T) {
	t.Parallel()
	const until sched.Time = 90_000
	cfg := faucetConfig(t, 23, 4)
	ref := newWorld(t, cfg)
	ref.mem = &memStore{log: ref.log}
	hooks := 0
	ref.run(until, 7, func(int) bool { hooks++; return false })
	want := ref.logBytes()
	if k := countKinds(t, ref.log); k[KindRefill] == 0 {
		t.Fatalf("no refill in the reference run: %v", k)
	}
	for at := 1; at <= hooks; at += max(hooks/25, 1) {
		w := newWorld(t, cfg)
		w.mem = &memStore{log: w.log}
		w.run(until, 7, func(n int) bool { return n == at })
		if !bytes.Equal(w.logBytes(), want) {
			t.Fatalf("crash at hook %d of %d: log differs from the uninterrupted run", at, hooks)
		}
	}
}

// A live engine started after its devnet runs the faucet's round due at
// time 0 on its first Tick, but sends the transaction to a node already at
// the Tick's slot: a TTL counted from time 0 is past there, and the node
// refuses the round with OutsideValidityIntervalUTxO, leaving every wallet
// unfunded until the next round.
func TestOverdueRefillGetsCurrentTTL(t *testing.T) {
	t.Parallel()
	cfg := faucetConfig(t, 3, 10)
	e, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	const late = 3_600_000
	n := 0
	for _, r := range mustHandle(t, e, tickInput(late)) {
		if r.Kind != KindRefill {
			continue
		}
		var f Refill
		if err := eventlog.Decode(r.Body, &f); err != nil {
			t.Fatal(err)
		}
		n++
		if now := cfg.Slots.SlotAt(late); f.TTL <= now {
			t.Fatalf("a refill sent at the tick of slot %d has TTL %d", now, f.TTL)
		}
	}
	if n == 0 {
		t.Fatal("no overdue refill was made")
	}
}
