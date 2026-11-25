package population

import (
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/chrisguiney/cardano-continuum/internal/profile"
	"github.com/chrisguiney/cardano-continuum/internal/rng"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

func walletProfile() profile.Profile {
	return profile.Profile{
		Name: "w", Kind: profile.Wallet,
		Traits: profile.Traits{
			Competence:   profile.Beta{A: 5, B: 3},
			Patience:     profile.Pareto{Xm: 50, Alpha: 1.3},
			LearningRate: profile.Exp{Rate: 10},
			Care:         profile.Beta{A: 4, B: 2},
		},
		Hazards: profile.Hazards{
			Birth:    0.01,
			Dormancy: profile.LogNormal{Mu: 2, Sigma: 0.8},
			Abandon:  0.0001,
		},
	}
}

func kindProfile(k profile.Kind) profile.Profile {
	p := walletProfile()
	p.Name, p.Kind = string(k), k
	return p
}

func seed(n byte) rng.Seed { return rng.Seed{n} }

func TestLifecycleTables(t *testing.T) {
	legal := map[profile.Kind][][2]State{
		profile.Wallet:   {{Born, Active}, {Active, Dormant}, {Dormant, Active}, {Active, Abandoned}, {Dormant, Abandoned}},
		profile.Pool:     {{Registered, Active}, {Active, Retiring}, {Retiring, Retired}, {Retiring, Active}},
		profile.DRep:     {{Registered, Voting}, {Voting, Expired}},
		profile.Contract: {{Deployed, Used}, {Used, Abandoned}},
	}
	states := []State{Born, Active, Dormant, Abandoned, Registered, Retiring, Retired, Voting, Expired, Deployed, Used}
	for kind, edges := range legal {
		ok := map[[2]State]bool{}
		for _, e := range edges {
			ok[e] = true
		}
		for _, from := range states {
			for _, to := range states {
				if got := Allowed(kind, from, to); got != ok[[2]State{from, to}] {
					t.Errorf("%s %s->%s: Allowed=%v want %v", kind, from, to, got, !got)
				}
			}
		}
	}
	for _, from := range states {
		for _, to := range states {
			if Allowed(profile.Faucet, from, to) {
				t.Errorf("faucet %s->%s allowed", from, to)
			}
		}
	}

	c := &Census{}
	a := c.Spawn(kindProfile(profile.Wallet), seed(1), 0)
	err := c.Apply(Transition{ID: a.ID, To: Dormant, At: 1, Cause: Timer{}})
	if !errors.Is(err, ErrIllegalTransition) || !strings.Contains(err.Error(), "born->dormant") {
		t.Fatalf("want illegal edge named, got %v", err)
	}
	if err := c.Apply(Transition{ID: 99, To: Active, Cause: Timer{}}); !errors.Is(err, ErrUnknownActor) {
		t.Fatalf("want unknown actor, got %v", err)
	}
}

func TestChainGatedTransitions(t *testing.T) {
	c := &Census{}
	p := c.Spawn(kindProfile(profile.Pool), seed(1), 0)
	if p.State != Registered {
		t.Fatalf("initial state %s", p.State)
	}
	err := c.Apply(Transition{ID: p.ID, To: Active, At: 5, Cause: Timer{}})
	if !errors.Is(err, ErrNotConfirmed) {
		t.Fatalf("timer cause: got %v", err)
	}
	if err := c.Apply(Transition{ID: p.ID, To: Active, At: 5, Cause: Confirmed{}}); !errors.Is(err, ErrNotConfirmed) {
		t.Fatalf("empty tx: got %v", err)
	}
	if got, _ := c.Get(p.ID); got.State != Registered {
		t.Fatalf("refused transition changed state to %s", got.State)
	}
	if err := c.Apply(Transition{ID: p.ID, To: Active, At: 5, Cause: Confirmed{Tx: "ab"}}); err != nil {
		t.Fatal(err)
	}
	if got, _ := c.Get(p.ID); got.State != Active || got.Changed != 5 {
		t.Fatalf("got %+v", got)
	}
	for _, k := range []struct {
		kind     profile.Kind
		from, to State
	}{{profile.Pool, Retiring, Retired}, {profile.DRep, Voting, Expired}} {
		if !ChainGated(k.kind, k.from, k.to) {
			t.Errorf("%s %s->%s should be chain gated", k.kind, k.from, k.to)
		}
	}
	if ChainGated(profile.Wallet, Active, Dormant) {
		t.Error("wallet dormancy is not chain gated")
	}
}

func TestSpawnDeterministic(t *testing.T) {
	p := walletProfile()
	a, b := &Census{}, &Census{}
	for i := 0; i < 50; i++ {
		if x, y := a.Spawn(p, seed(7), 10), b.Spawn(p, seed(7), 10); x != y {
			t.Fatalf("actor %d differs: %+v vs %+v", i, x, y)
		}
	}
	c := &Census{}
	if x, y := a.mustGet(1), c.Spawn(p, seed(8), 10); x.Traits == y.Traits {
		t.Fatal("different seeds gave identical traits")
	}
	if got := a.mustGet(1); got.Competence != got.Traits.Competence || got.Born != 10 || got.Changed != 10 {
		t.Fatalf("bad initial actor %+v", got)
	}
}

func (c *Census) mustGet(id ActorID) Actor {
	a, ok := c.Get(id)
	if !ok {
		panic("missing actor")
	}
	return a
}

func TestSpawnAppendOnly(t *testing.T) {
	p := walletProfile()
	a, b := &Census{}, &Census{}
	for i := 0; i < 20; i++ {
		a.Spawn(p, seed(3), 0)
	}
	for i := 0; i < 21; i++ {
		b.Spawn(p, seed(3), 0)
	}
	for id := ActorID(1); id <= 20; id++ {
		if a.mustGet(id) != b.mustGet(id) {
			t.Fatalf("actor %d changed when actor 21 was added", id)
		}
	}
}

func ksLogNormal(xs []float64, mu, sigma float64) float64 {
	sort.Float64s(xs)
	n := float64(len(xs))
	d := 0.0
	for i, x := range xs {
		f := 0.5 * (1 + math.Erf((math.Log(x)-mu)/(sigma*math.Sqrt2)))
		d = math.Max(d, math.Max(f-float64(i)/n, float64(i+1)/n-f))
	}
	return d
}

func TestDormancyLogNormal(t *testing.T) {
	p := walletProfile()
	p.Hazards.Abandon = 0
	const n = 20000
	r := seed(1).Rand()
	a := Actor{Kind: profile.Wallet, State: Dormant}
	xs := make([]float64, n)
	for i := range xs {
		at, to := NextHazard(a, p, r, 0)
		if to != Active {
			t.Fatalf("dormant actor heads to %s", to)
		}
		xs[i] = float64(at) / 1000
	}
	// Critical value at alpha=0.001 is 1.95/sqrt(n); ms rounding adds a negligible offset.
	if d := ksLogNormal(xs, 2, 0.8); d > 1.95/math.Sqrt(n) {
		t.Fatalf("KS D=%.5f rejects log-normal(2,0.8)", d)
	}
}

func TestAbandonCompetesWithDormancyEnd(t *testing.T) {
	p := walletProfile()
	p.Hazards.Abandon = 0.5
	r := seed(2).Rand()
	a := Actor{Kind: profile.Wallet, State: Dormant}
	abandoned := 0
	for i := 0; i < 2000; i++ {
		if _, to := NextHazard(a, p, r, 0); to == Abandoned {
			abandoned++
		}
	}
	if abandoned == 0 || abandoned == 2000 {
		t.Fatalf("abandoned %d of 2000", abandoned)
	}
}

func TestActivityPareto(t *testing.T) {
	p := walletProfile()
	c := &Census{}
	const n = 20000
	sum := 0.0
	for i := 0; i < n; i++ {
		a := c.Spawn(p, seed(4), 0)
		if a.Traits.RateMult < 1 {
			t.Fatalf("multiplier %v below scale", a.Traits.RateMult)
		}
		sum += math.Log(a.Traits.RateMult)
	}
	alpha := profile.DefaultTail
	if hat := n / sum; math.Abs(hat-alpha)/alpha > 0.03 {
		t.Fatalf("Hill alpha %.3f, want %.3f", hat, alpha)
	}
}

// TestActivityTailFromProfile: the profile's tail index, not a package
// constant, shapes the activity multiplier.
func TestActivityTailFromProfile(t *testing.T) {
	p := walletProfile()
	p.Rates.Tail = 3
	c := &Census{}
	const n = 20000
	sum := 0.0
	for i := 0; i < n; i++ {
		sum += math.Log(c.Spawn(p, seed(4), 0).Traits.RateMult)
	}
	if hat := n / sum; math.Abs(hat-3)/3 > 0.03 {
		t.Fatalf("Hill alpha %.3f, want 3", hat)
	}
}

// TestDormancyOnsetFromProfile: a profile with a faster onset goes dormant
// sooner, at the rate it names.
func TestDormancyOnsetFromProfile(t *testing.T) {
	p := walletProfile()
	p.Hazards.Abandon = 0
	p.Hazards.Onset = 0.01
	r := seed(6).Rand()
	a := Actor{Kind: profile.Wallet, State: Active}
	s := 0.0
	const n = 20000
	for i := 0; i < n; i++ {
		at, _ := NextHazard(a, p, r, 0)
		s += float64(at) / 1000
	}
	if mean := s / n; math.Abs(mean-100)/100 > 0.03 {
		t.Fatalf("mean onset %.2fs, want 100s", mean)
	}
}

func TestFrustrationRaisesHazard(t *testing.T) {
	prev := -1.0
	for f := 0.0; f <= 5; f += 0.25 {
		r := DormancyOnsetRate(profile.DefaultOnset, f)
		if r <= prev {
			t.Fatalf("rate %v at frustration %v not above %v", r, f, prev)
		}
		prev = r
	}
	if DormancyOnsetRate(profile.DefaultOnset, -1) != DormancyOnsetRate(profile.DefaultOnset, 0) {
		t.Fatal("negative frustration must clamp to zero")
	}
	p := walletProfile()
	p.Hazards.Abandon = 0
	mean := func(f float64) float64 {
		r := seed(5).Rand()
		a := Actor{Kind: profile.Wallet, State: Active, Frustration: f}
		s := 0.0
		for i := 0; i < 5000; i++ {
			at, to := NextHazard(a, p, r, 0)
			if to != Dormant {
				t.Fatalf("active actor heads to %s", to)
			}
			s += float64(at)
		}
		return s / 5000
	}
	if lo, hi := mean(0), mean(3); hi >= lo {
		t.Fatalf("mean time to dormancy %v at f=3 not below %v at f=0", hi, lo)
	}
}

func TestNextHazardOtherStates(t *testing.T) {
	p := walletProfile()
	r := seed(1).Rand()
	at, to := NextHazard(Actor{Kind: profile.Wallet, State: Born}, p, r, 100)
	if to != Active || at != 101 {
		t.Fatalf("born: %v %v", at, to)
	}
	for _, a := range []Actor{
		{Kind: profile.Pool, State: Registered}, {Kind: profile.DRep, State: Voting},
		{Kind: profile.Wallet, State: Abandoned}, {Kind: profile.Contract, State: Deployed},
	} {
		if at, to := NextHazard(a, p, r, 0); at != Never || to != a.State {
			t.Errorf("%s/%s: %v %v", a.Kind, a.State, at, to)
		}
	}
	if _, to := NextHazard(Actor{Kind: profile.Contract, State: Used}, p, r, 0); to != Abandoned {
		t.Errorf("used contract heads to %s", to)
	}
	q := p
	q.Hazards = profile.Hazards{}
	if at, _ := NextHazard(Actor{Kind: profile.Wallet, State: Active}, q, r, 0); at != Never {
		t.Errorf("no hazards: %v", at)
	}
	if at := NextBirth(r, q, 0); at != Never {
		t.Errorf("zero birth rate: %v", at)
	}
	if at := NextBirth(r, p, 50); at <= 50 || at == Never {
		t.Errorf("birth at %v", at)
	}
}

func TestPreferentialAttachment(t *testing.T) {
	stake := []PoolStake{{1, 1000}, {2, 3000}, {3, 6000}, {4, 0}}
	const n, floor = 50000, 100
	r := seed(1).Rand()
	hits := map[ActorID]int{}
	for i := 0; i < n; i++ {
		id, ok := Attach(r, stake, floor)
		if !ok {
			t.Fatal("no pick")
		}
		hits[id]++
	}
	total := 1000.0 + 3000 + 6000 + floor
	for _, s := range stake {
		w := math.Max(float64(s.Stake), floor)
		want := w / total * n
		if math.Abs(float64(hits[s.ID])-want) > 5*math.Sqrt(want) {
			t.Errorf("pool %d: %d picks, want ~%.0f", s.ID, hits[s.ID], want)
		}
	}
	if hits[4] == 0 {
		t.Error("zero-stake pool never picked")
	}
	if _, ok := Attach(r, nil, floor); ok {
		t.Error("empty pool set picked")
	}
	if _, ok := Attach(r, []PoolStake{{1, 0}}, 0); ok {
		t.Error("zero total weight picked")
	}
}

func TestCounts(t *testing.T) {
	c := &Census{}
	kinds := []profile.Kind{profile.Wallet, profile.Pool, profile.DRep, profile.Contract, profile.Faucet}
	for i := 0; i < 400; i++ {
		c.Spawn(kindProfile(kinds[i%len(kinds)]), seed(9), 0)
	}
	r := seed(10).Rand()
	applied := 0
	for i := 0; i < 10000; i++ {
		id := ActorID(r.IntN(c.Len()) + 1)
		a := c.mustGet(id)
		var next []State
		for _, s := range allStates {
			if Allowed(a.Kind, a.State, s) {
				next = append(next, s)
			}
		}
		if len(next) == 0 {
			continue
		}
		to := next[r.IntN(len(next))]
		cause := Cause(Timer{})
		switch {
		case a.Kind == profile.Pool && to == Retired:
			cause = EpochApplied{Epoch: 1}
		case ChainGated(a.Kind, a.State, to):
			cause = Confirmed{Tx: "tx"}
		}
		if err := c.Apply(Transition{ID: id, To: to, At: sched.Time(i), Cause: cause}); err != nil {
			t.Fatal(err)
		}
		applied++
	}
	if applied < 500 {
		t.Fatalf("only %d transitions applied", applied)
	}
	for _, k := range kinds {
		for _, s := range allStates {
			want := 0
			ids := c.IDs(k, s)
			for id := ActorID(1); id <= ActorID(c.Len()); id++ {
				if a := c.mustGet(id); a.Kind == k && a.State == s {
					want++
				}
			}
			if got := c.Count(k, s); got != want || len(ids) != want || !sort.SliceIsSorted(ids, func(i, j int) bool { return ids[i] < ids[j] }) {
				t.Errorf("%s/%s: Count=%d IDs=%d scan=%d", k, s, got, len(ids), want)
			}
		}
	}
}

func TestSetFrustration(t *testing.T) {
	c := &Census{}
	a := c.Spawn(walletProfile(), seed(1), 0)
	if err := c.SetFrustration(a.ID, 2.5); err != nil {
		t.Fatal(err)
	}
	if err := c.SetFrustration(a.ID, -1); err != nil {
		t.Fatal(err)
	}
	if got := c.mustGet(a.ID); got.Frustration != 0 {
		t.Fatalf("frustration %v", got.Frustration)
	}
	if err := c.SetFrustration(77, 1); !errors.Is(err, ErrUnknownActor) {
		t.Fatal(err)
	}
}

type logEntry struct {
	At sched.Time
	ID ActorID
	To State
}

// run drives a census of n wallets to horizon from one root seed, with each
// actor consuming only its own hazard stream, and returns the event log.
func run(n int, horizon sched.Time) []logEntry {
	root := seed(42)
	p := walletProfile()
	p.Hazards.Abandon = 0.00002
	c := &Census{}
	var q sched.Queue
	streams := map[ActorID]*rand.Rand{}
	sched1 := func(a Actor, now sched.Time) {
		at, to := NextHazard(a, p, streams[a.ID], now)
		if at != Never && at <= horizon {
			q.Push(at, Transition{ID: a.ID, To: to, Cause: Timer{}})
		}
	}
	for i := 0; i < n; i++ {
		a := c.Spawn(p, root, 0)
		streams[a.ID] = a.Stream("hazard")
		sched1(a, 0)
	}
	var log []logEntry
	for {
		at, ev, ok := q.Pop()
		if !ok {
			return log
		}
		tr := ev.(Transition)
		tr.At = at
		if err := c.Apply(tr); err != nil {
			panic(fmt.Sprint(err))
		}
		log = append(log, logEntry{at, tr.ID, tr.To})
		sched1(c.mustGet(tr.ID), at)
	}
}

func TestRunDeterministic(t *testing.T) {
	const horizon = 10 * 24 * 3600 * 1000
	a, b := run(30, horizon), run(30, horizon)
	if len(a) < 100 {
		t.Fatalf("only %d events", len(a))
	}
	if fmt.Sprint(a) != fmt.Sprint(b) {
		t.Fatal("same seed produced different event sequences")
	}
}

func TestAddingActorLeavesOthersUnchanged(t *testing.T) {
	const horizon = 10 * 24 * 3600 * 1000
	small, big := run(30, horizon), run(31, horizon)
	var got []logEntry
	for _, e := range big {
		if e.ID <= 30 {
			got = append(got, e)
		}
	}
	if fmt.Sprint(small) != fmt.Sprint(got) {
		t.Fatal("actor 31 changed the history of actors 1..30")
	}
}

// activePool spawns a pool and confirms its registration.
func activePool(t *testing.T, c *Census) Actor {
	t.Helper()
	p := c.Spawn(kindProfile(profile.Pool), seed(1), 0)
	if err := c.Apply(Transition{ID: p.ID, To: Active, At: 1, Cause: Confirmed{Tx: "reg"}}); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestPoolRetiringNeedsConfirmation: a pool is Retiring only once its
// retirement certificate confirms, never on the engine's decision alone.
func TestPoolRetiringNeedsConfirmation(t *testing.T) {
	c := &Census{}
	p := activePool(t, c)
	for _, cause := range []Cause{Timer{}, Intent{}, Confirmed{}} {
		if err := c.Apply(Transition{ID: p.ID, To: Retiring, At: 2, Cause: cause}); !errors.Is(err, ErrNotConfirmed) {
			t.Fatalf("cause %T: got %v", cause, err)
		}
	}
	if err := c.Apply(Transition{ID: p.ID, To: Retiring, At: 2, Cause: Confirmed{Tx: "retire"}}); err != nil {
		t.Fatal(err)
	}
	if got, _ := c.Get(p.ID); got.State != Retiring {
		t.Fatalf("state %s", got.State)
	}
}

// TestPoolRetiredOnlyAtEpochApply: the certificate's confirmation does not
// retire a pool; only the epoch transition that applies it does.
func TestPoolRetiredOnlyAtEpochApply(t *testing.T) {
	c := &Census{}
	p := activePool(t, c)
	if err := c.Apply(Transition{ID: p.ID, To: Retiring, At: 2, Cause: Confirmed{Tx: "retire"}}); err != nil {
		t.Fatal(err)
	}
	for _, cause := range []Cause{Timer{}, Intent{}, Confirmed{Tx: "retire"}} {
		if err := c.Apply(Transition{ID: p.ID, To: Retired, At: 3, Cause: cause}); !errors.Is(err, ErrNotConfirmed) {
			t.Fatalf("cause %T: got %v", cause, err)
		}
	}
	if err := c.Apply(Transition{ID: p.ID, To: Retired, At: 3, Cause: EpochApplied{Epoch: 5}}); err != nil {
		t.Fatal(err)
	}
	if got, _ := c.Get(p.ID); got.State != Retired {
		t.Fatalf("state %s", got.State)
	}
	if ChainGated(profile.Pool, Retiring, Retired) != true {
		t.Fatal("retirement must be chain gated")
	}
}

// TestPoolReregisterCancelsRetiring: a confirmed re-registration takes a
// retiring pool back to Active; nothing else does.
func TestPoolReregisterCancelsRetiring(t *testing.T) {
	c := &Census{}
	p := activePool(t, c)
	if err := c.Apply(Transition{ID: p.ID, To: Retiring, At: 2, Cause: Confirmed{Tx: "retire"}}); err != nil {
		t.Fatal(err)
	}
	for _, cause := range []Cause{Timer{}, Intent{}, EpochApplied{Epoch: 3}} {
		if err := c.Apply(Transition{ID: p.ID, To: Active, At: 3, Cause: cause}); !errors.Is(err, ErrNotConfirmed) {
			t.Fatalf("cause %T: got %v", cause, err)
		}
	}
	if err := c.Apply(Transition{ID: p.ID, To: Active, At: 3, Cause: Confirmed{Tx: "rereg"}}); err != nil {
		t.Fatal(err)
	}
	if got, _ := c.Get(p.ID); got.State != Active {
		t.Fatalf("state %s", got.State)
	}
}

// TestCensusNotAStakeSource: the census offers nothing a stake-floor guard
// could take a denominator from. A retired pool's stake stays in the leader
// schedule for two epochs, so stake must come from the ledger's snapshots.
func TestCensusNotAStakeSource(t *testing.T) {
	typ := reflect.TypeOf(&Census{})
	for i := 0; i < typ.NumMethod(); i++ {
		if m := typ.Method(i); strings.Contains(strings.ToLower(m.Name), "stake") {
			t.Errorf("Census.%s looks like a stake source", m.Name)
		}
	}
	at := reflect.TypeOf(Actor{})
	for i := 0; i < at.NumField(); i++ {
		if f := at.Field(i); strings.Contains(strings.ToLower(f.Name), "stake") {
			t.Errorf("Actor.%s looks like a stake source", f.Name)
		}
	}
}
