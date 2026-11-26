package adversary

import (
	"fmt"
	"slices"

	"github.com/chrisguiney/cardano-continuum/internal/agenda"
	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
)

// Ref names a transaction output.
type Ref struct {
	Tx    [32]byte `cbor:"1,keyasint"`
	Index uint32   `cbor:"2,keyasint"`
}

// Locked is an output at the engine's shared script address that an honest
// actor locked. Busy reports that an honest spend of it is in flight.
type Locked struct {
	Ref      Ref
	Owner    population.ActorID
	Lovelace uint64
	Busy     bool
}

// Coin is an honest actor's key-locked output.
type Coin struct {
	Ref      Ref
	Owner    population.ActorID
	Lovelace uint64
}

// Sight is what a plan reads of the world.
type Sight interface {
	// Shared lists the honest locked outputs at the shared script
	// address, by Ref.
	Shared() []Locked
	// Payees lists the honest wallets, ascending.
	Payees() []population.ActorID
	// Victims lists honest key-locked outputs, by Ref.
	Victims() []Coin
}

// View is the agenda view with the adversary's side: what it sees and
// what is left of its cost ceiling this epoch. The engine implements it;
// a view without it scores no attack.
type View interface {
	agenda.View
	Sight
	Remaining(population.ActorID) uint64
}

// attackValue outweighs the cover agendas' idle score of 1, so an
// adversary that can attack mostly does.
const attackValue = 3

// cost is an attack's lovelace estimate: fee and value given away (spend)
// and what its inputs must hold (need). The policy goes idle when the
// ceiling or the balance cannot cover one more step; the engine charges
// the exact cost of what it built.
type cost struct{ spend, need uint64 }

var costs = map[Name]cost{
	DustFlood:   {spend: minDust * 1_000_000, need: maxDust*1_200_000 + 2_000_000},
	Contention:  {spend: 1_000_000, need: 6_000_000},
	Malformed:   {spend: StealFee, need: 4_000_000},
	FrontRun:    {spend: 1_000_000, need: 6_000_000},
	DoubleSpend: {spend: 2_000_000, need: 4_000_000},
}

// Policy scores an adversary's attacks. It is the agenda.Policy that
// agenda.WithAttacks takes.
type Policy struct {
	attacks []Name
}

// NewPolicy validates spec's attacks and success metric.
func NewPolicy(spec profile.AdversarySpec) (Policy, error) {
	var p Policy
	for _, s := range spec.Attacks {
		n, err := Parse(s)
		if err != nil {
			return Policy{}, err
		}
		if !slices.Contains(p.attacks, n) {
			p.attacks = append(p.attacks, n)
		}
	}
	if m := spec.SuccessMetric; m != "" && !slices.Contains(MetricNames(), m) {
		return Policy{}, fmt.Errorf("%w success metric %q", ErrUnknown, m)
	}
	slices.Sort(p.attacks)
	return p, nil
}

// Has reports whether the policy includes attack n.
func (p Policy) Has(n Name) bool { return slices.Contains(p.attacks, n) }

// Score implements agenda.Policy.
func (p Policy) Score(a population.Actor, v agenda.View) []agenda.Score {
	av, ok := v.(View)
	out := make([]agenda.Score, 0, len(p.attacks))
	for _, n := range p.attacks {
		if !ok {
			out = append(out, off(n, "no adversary view"))
			continue
		}
		out = append(out, score(n, a.ID, av))
	}
	return out
}

func score(n Name, id population.ActorID, v View) agenda.Score {
	c := costs[n]
	switch {
	case n == FrontRun:
		return off(n, "reacts to honest unlocks; never chosen")
	case v.Remaining(id) < c.spend:
		return off(n, fmt.Sprintf("cost ceiling: %d left this epoch", v.Remaining(id)))
	case uint64(v.Balance(id)) < c.need:
		return off(n, fmt.Sprintf("balance %d below %d", v.Balance(id), c.need))
	case n == Contention && !slices.ContainsFunc(v.Shared(), func(l Locked) bool { return !l.Busy }):
		return off(n, "no shared output to contend for")
	case n == DoubleSpend && len(v.Payees()) < 2:
		return off(n, "fewer than two payees")
	}
	return agenda.Score{Kind: n.Kind(), Value: attackValue, Why: "attack"}
}

func off(n Name, why string) agenda.Score { return agenda.Score{Kind: n.Kind(), Why: why} }
