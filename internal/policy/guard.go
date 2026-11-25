package policy

import (
	"cmp"
	"errors"
	"fmt"
	"math/bits"
	"slices"

	"github.com/chrisguiney/cardano-continuum/internal/population"
)

// Config is the floor the guard keeps.
type Config struct {
	// Floor is the percentage (1 to 100) of a schedule's delegated stake
	// that forging pools must hold.
	Floor uint64
	// Margin is how many forging pools may die at once with the floor still
	// held; the largest are assumed lost. At least 1: recovery cannot stand
	// in for margin, since below the floor nothing promises a block and an
	// epoch with no block stalls the ledger for good.
	Margin int
}

// Guard decides whether a step keeps the stake floor. The zero value is not
// usable; build one with New.
type Guard struct{ cfg Config }

// New returns a guard for cfg.
func New(cfg Config) (Guard, error) {
	if cfg.Floor < 1 || cfg.Floor > 100 {
		return Guard{}, fmt.Errorf("policy: floor %d%% outside 1..100", cfg.Floor)
	}
	if cfg.Margin < 1 {
		return Guard{}, fmt.Errorf("policy: fault margin %d below 1", cfg.Margin)
	}
	return Guard{cfg: cfg}, nil
}

// Verdict is the guard's answer. Why explains it either way.
type Verdict struct {
	OK  bool
	Why string
}

func refuse(format string, a ...any) Verdict {
	return Verdict{Why: fmt.Sprintf(format, a...)}
}

// Check reports whether the engine may take step in in state s. GoLive,
// Repair, Reregister and a delegation from a pool that does not forge to
// one that does are always allowed, so recovery is never blocked. Every
// other step is allowed only if each schedule it can still reach keeps the
// floor after losing the Margin largest forging pools.
func (g Guard) Check(s State, in Intent) Verdict {
	switch in.Op {
	case GoLive, Repair, Reregister:
		return Verdict{OK: true, Why: in.Op.String() + " only adds forging stake"}
	case Delegate:
		if adds(s, in) {
			return Verdict{OK: true, Why: "moves stake onto a forging pool from one that does not forge"}
		}
	case GoDark, Retire, Undelegate:
	default:
		return refuse("unknown step %d", int(in.Op))
	}
	proj, err := project(s.Deleg, append(slices.Clip(s.InFlight), in))
	if err != nil {
		return refuse("%s: %v", in.Op, err)
	}
	ws, err := s.horizon(proj)
	if err != nil {
		return refuse("%s: %v", in.Op, err)
	}
	f := s.forging(&in)
	for _, w := range ws {
		if v := g.meets(w, f); !v.OK {
			return refuse("%s refused: %s", in.Op, v.Why)
		}
	}
	return Verdict{OK: true, Why: fmt.Sprintf("%s keeps %d%% in every schedule", in.Op, g.cfg.Floor)}
}

// adds reports a delegation that only moves stake from a pool the guard
// does not count as forging onto one it does: the total is unchanged and
// the forging share only grows, in every schedule (the model's Adds). An
// undelegated wallet joining a forging pool raises the total too, so it is
// guarded like any other move.
func adds(s State, in Intent) bool {
	f := s.forging(nil)
	for _, e := range s.Deleg {
		if e.Wallet == in.Wallet {
			return f[in.Pool] && e.Pool != 0 && !f[e.Pool]
		}
	}
	return false
}

// window is one schedule the guard checks, with its name for a refusal.
type window struct {
	name string
	d    Schedule
}

// horizon lists the schedules a decision taken now can still reach. They
// depend on whether this epoch's transition has run, not on the epoch
// number: before the epoch's first block the schedule in use is already
// the mark the transition will install, and the next one is today's
// delegation. The projection is the confirmed delegation with everything in
// flight landed. Later schedules differ from the projection only by the
// stake of retired pools, which the guard already counts as dark.
func (s State) horizon(proj Schedule) ([]window, error) {
	switch {
	case s.Label > s.Epoch:
		return nil, fmt.Errorf("last transition epoch %d is after epoch %d", s.Label, s.Epoch)
	case s.Frozen():
		return []window{{"set", s.Set}}, nil
	case s.Label == s.Epoch:
		return []window{{"set", s.Set}, {"mark", s.Mark}, {"projection", proj}}, nil
	}
	return []window{{"mark", s.Mark}, {"delegation", s.Deleg}, {"projection", proj}}, nil
}

var errNoEntry = errors.New("wallet has no confirmed stake entry")

// project applies every delegation and undelegation in txs to the
// confirmed delegation d, in order, as if all of them land.
func project(d Schedule, txs []Intent) (Schedule, error) {
	out := slices.Clone(d)
	idx := make(map[population.ActorID]int, len(out))
	for i, e := range out {
		idx[e.Wallet] = i
	}
	for _, tx := range txs {
		if tx.Op != Delegate && tx.Op != Undelegate {
			continue
		}
		i, ok := idx[tx.Wallet]
		if !ok {
			return nil, fmt.Errorf("%w: wallet %d", errNoEntry, tx.Wallet)
		}
		out[i].Pool = 0
		if tx.Op == Delegate {
			out[i].Pool = tx.Pool
		}
	}
	return out, nil
}

// meets checks one schedule: the stake on forging pools, less the Margin
// largest of them, is at least Floor percent of all delegated stake in the
// schedule. The total comes from the schedule itself, never from the
// census, so a retired pool's stake stays in it while the snapshot holds it.
func (g Guard) meets(w window, f map[population.ActorID]bool) Verdict {
	seen := make(map[population.ActorID]bool, len(w.d))
	byPool := map[population.ActorID]uint64{}
	var tot uint64
	for _, e := range w.d {
		if seen[e.Wallet] {
			return refuse("%s lists wallet %d twice", w.name, e.Wallet)
		}
		seen[e.Wallet] = true
		if e.Pool == 0 {
			continue
		}
		var carry uint64
		tot, carry = bits.Add64(tot, e.Stake, 0)
		if carry != 0 {
			return refuse("%s total stake overflows", w.name)
		}
		if f[e.Pool] {
			byPool[e.Pool] += e.Stake // bounded by tot, which did not overflow
		}
	}
	fs := sumAfterLoss(byPool, g.cfg.Margin)
	if tot == 0 {
		return refuse("%s has no delegated stake", w.name)
	}
	// 100*fs >= Floor*tot, in 128 bits.
	lh, ll := bits.Mul64(100, fs)
	rh, rl := bits.Mul64(g.cfg.Floor, tot)
	if lh < rh || (lh == rh && ll < rl) {
		return refuse("%s: forging %d of %d after losing %d pools, below %d%%",
			w.name, fs, tot, g.cfg.Margin, g.cfg.Floor)
	}
	return Verdict{OK: true}
}

// sumAfterLoss is the stake left after the margin largest pools die. Ties
// in size do not change the sum, so the order is only made total for
// readability.
func sumAfterLoss(byPool map[population.ActorID]uint64, margin int) uint64 {
	type ps struct {
		id    population.ActorID
		stake uint64
	}
	all := make([]ps, 0, len(byPool))
	for id, st := range byPool {
		all = append(all, ps{id, st})
	}
	slices.SortFunc(all, func(a, b ps) int {
		if c := cmp.Compare(b.stake, a.stake); c != 0 {
			return c
		}
		return cmp.Compare(a.id, b.id)
	})
	var sum uint64
	for i, p := range all {
		if i >= margin {
			sum += p.stake
		}
	}
	return sum
}
