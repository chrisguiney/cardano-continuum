package policy

import (
	"math/rand/v2"
	"reflect"
	"testing"

	"github.com/chrisguiney/cardano-continuum/internal/population"
)

// randState is a state over five pools and eight wallets with stakes up to
// 1000 ADA, larger than the model's domain.
func randState(r *rand.Rand) State {
	const pools, wallets = 5, 8
	s := State{Label: uint64(r.IntN(4))}
	s.Epoch = s.Label + uint64(r.IntN(2))
	stake := make([]uint64, wallets)
	for w := range stake {
		stake[w] = uint64(r.IntN(1000)+1) * 1_000_000
	}
	sched := func() Schedule {
		out := make(Schedule, wallets)
		for w := range out {
			out[w] = Delegation{Wallet: population.ActorID(100 + w), Pool: population.ActorID(r.IntN(pools + 1)), Stake: stake[w]}
		}
		return out
	}
	s.Set, s.Mark, s.Deleg = sched(), sched(), sched()
	for p := 1; p <= pools; p++ {
		s.Pools = append(s.Pools, Pool{ID: population.ActorID(p), Reg: []Reg{Registered, Registered, Registered, Retiring}[r.IntN(4)],
			Live: r.IntN(4) != 0, Running: r.IntN(6) != 0})
	}
	if r.IntN(3) == 0 {
		s.InFlight = append(s.InFlight, Intent{Op: Delegate, Wallet: population.ActorID(100 + r.IntN(wallets)), Pool: population.ActorID(r.IntN(pools) + 1)})
	}
	return s
}

func randIntent(r *rand.Rand) Intent {
	p := population.ActorID(r.IntN(5) + 1)
	w := population.ActorID(100 + r.IntN(8))
	return []Intent{{Op: GoDark, Pool: p}, {Op: Retire, Pool: p}, {Op: Delegate, Wallet: w, Pool: p}, {Op: Undelegate, Wallet: w}}[r.IntN(4)]
}

// TestPropGuardMonotone: an allowed decision stays allowed when a pool goes
// live or a wallet moves onto a forging pool from one that does not forge,
// except where that changes the decision's own classification (its pool,
// its wallet, or the pool the wallet leaves). A wallet that was delegated
// nowhere is left out: it raises the total, and with a margin the pool it
// joins can become the largest one assumed lost, so the share can fall.
func TestPropGuardMonotone(t *testing.T) {
	r := rand.New(rand.NewPCG(31, 2))
	g := guard(40, 1)
	tried := 0
	for range 40_000 {
		s, in := randState(r), randIntent(r)
		if !g.Check(s, in).OK {
			continue
		}
		tried++
		from := entry(s.Deleg, in.Wallet).Pool
		for i := range s.Pools {
			p := s.Pools[i].ID
			if p == in.Pool || p == from || s.Pools[i].Live {
				continue
			}
			up := s
			up.Pools = append([]Pool(nil), s.Pools...)
			up.Pools[i].Live = true
			if v := g.Check(up, in); !v.OK {
				t.Fatalf("pool %d going live refused %+v: %+v", p, in, v)
			}
		}
		f := s.forging(nil)
		for i, e := range s.Deleg {
			if e.Wallet == in.Wallet || e.Pool == 0 || f[e.Pool] {
				continue
			}
			for p := range f {
				up := s
				up.Deleg = append(Schedule(nil), s.Deleg...)
				up.Deleg[i].Pool = p
				if v := g.Check(up, in); !v.OK {
					t.Fatalf("wallet %d moving to forging pool %d refused %+v: %+v", e.Wallet, p, in, v)
				}
			}
		}
	}
	if tried < 1000 {
		t.Fatalf("only %d allowed decisions tried", tried)
	}
}

func entry(s Schedule, w population.ActorID) Delegation {
	for _, e := range s {
		if e.Wallet == w {
			return e
		}
	}
	return Delegation{}
}

func TestEndangeredOnProjectedBreach(t *testing.T) {
	g := guard(70, 1)
	if g.Endangered(base()) {
		t.Fatal("genesis endangered")
	}
	// Stake moved onto C this epoch, then C's pod dies: the schedules in
	// use and next still meet the floor, the projection does not.
	s := base()
	s.Deleg = moved(moved(s.Deleg, 13, pC), 14, pC)
	pool(&s, pC).Running = false
	// set and mark: A 3 + B 2 of 6 = 83%; projection: A 3 of 6 = 50%.
	if !g.Endangered(s) {
		t.Fatal("projected breach not seen")
	}
	if p := g.Recover(s); len(p.Repair) == 0 && len(p.Moves) == 0 {
		t.Fatalf("no recovery on a projected breach: %+v", p)
	}
}

func TestRecoverPlan(t *testing.T) {
	g := guard(70, 1)
	s := base()
	s.Pools = append(s.Pools,
		Pool{ID: 4, Reg: Registered, Live: false, Running: true}, // dark: bring live
		Pool{ID: 5, Reg: Registered, Live: false, Running: true}, // dark, retirement in flight: leave
		Pool{ID: 6, Reg: Retiring, Live: false, Running: false},  // retiring: leave
	)
	pool(&s, pA).Running = false // dead pod: repair, move w11 off
	pool(&s, pB).Live = false    // dark: bring live, keep its wallets
	s.Deleg = append(s.Deleg, d(15, 6, 2), d(16, 5, 1), d(17, 6, 1))
	s.InFlight = []Intent{{Op: Retire, Pool: 5}, {Op: Delegate, Wallet: 17, Pool: 0}}
	s.Set, s.Mark = s.Deleg, s.Deleg
	if !g.Endangered(s) {
		t.Fatal("not endangered")
	}
	got := g.Recover(s)
	want := Plan{
		GoLive: []population.ActorID{pB, 4},
		Repair: []population.ActorID{pA},
		// Targets are the pools forging once B and 4 are live, least
		// projected stake first, ties to the lower ID: B 2, C 1, 4 0.
		// w11 (3) goes to 4, w15 (2) to C, then w16 (1) to B.
		Moves: []Intent{
			{Op: Delegate, Wallet: 11, Pool: 4},
			{Op: Delegate, Wallet: 15, Pool: pC},
			{Op: Delegate, Wallet: 16, Pool: pB},
		},
		Waiting: []population.ActorID{17},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %+v\nwant %+v", got, want)
	}
	// The plan does not depend on input order.
	r := s
	r.Pools = append([]Pool(nil), s.Pools...)
	r.Deleg = append(Schedule(nil), s.Deleg...)
	r.Pools[0], r.Pools[5] = r.Pools[5], r.Pools[0]
	r.Deleg[0], r.Deleg[6] = r.Deleg[6], r.Deleg[0]
	if again := g.Recover(r); !reflect.DeepEqual(again, want) {
		t.Fatalf("order dependent: %+v", again)
	}
}

func TestRecoverQuietWhenSafe(t *testing.T) {
	s := base()
	pool(&s, pC).Live = false
	if p := guard(70, 1).Recover(s); !reflect.DeepEqual(p, Plan{}) {
		t.Fatalf("plan on a safe state: %+v", p)
	}
	s.Epoch, s.Label = 5, 2
	if !s.Frozen() {
		t.Fatal("a skipped epoch is a frozen ledger")
	}
}
