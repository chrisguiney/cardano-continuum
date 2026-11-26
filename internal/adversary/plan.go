package adversary

import (
	"math/rand/v2"

	"github.com/chrisguiney/cardano-continuum/internal/population"
)

// Dust plan bounds: outputs per flood.
const (
	minDust = 10
	maxDust = 30
	// dustScript is the share of dust paid to the shared script address,
	// which every unlocker scans; the rest goes to honest wallets, whose
	// coin selection then wades through it.
	dustScript = 0.5
)

// Payee is one output of a step: an honest wallet, or the shared script
// address.
type Payee struct {
	Actor  population.ActorID
	Script bool
}

// Step is one planned attack. Which fields are set depends on Attack:
//
//	dust-flood       Payees: one min-UTxO output each, in one transaction
//	utxo-contention  Spend
//	front-run        Spend and Tip, the lovelace bid above the minimum fee
//	malformed-cbor   Mutation, and Victim for a Steal
//	double-spend     Payees: one transaction each, all on the same inputs
type Step struct {
	Attack   Name
	Payees   []Payee
	Spend    *Locked
	Tip      uint64
	Mutation Mutation
	Victim   *Coin
}

// Plan draws attack n's next step from r. A non-empty why says there is
// nothing to attack.
func Plan(n Name, s Sight, r *rand.Rand) (Step, string) {
	switch n {
	case DustFlood:
		return planDust(s, r), ""
	case Contention:
		return planContention(s, r)
	case Malformed:
		return planMalformed(s, r), ""
	case DoubleSpend:
		return planRace(s, r)
	}
	return Step{}, "attack " + string(n) + " has no plan of its own"
}

// Outbid is the front-run step that outbids an honest spend of target paying
// honestFee: the same output, a fee strictly higher.
func Outbid(target Locked, honestFee uint64) Step {
	return Step{Attack: FrontRun, Spend: &target, Tip: honestFee + 1}
}

func planDust(s Sight, r *rand.Rand) Step {
	payees := s.Payees()
	n := minDust + r.IntN(maxDust-minDust+1)
	st := Step{Attack: DustFlood, Payees: make([]Payee, 0, n)}
	for range n {
		if len(payees) == 0 || r.Float64() < dustScript {
			st.Payees = append(st.Payees, Payee{Script: true})
			continue
		}
		st.Payees = append(st.Payees, Payee{Actor: payees[r.IntN(len(payees))]})
	}
	return st
}

// planContention takes a shared output its honest owner has not yet
// moved to spend: the owner's spend then meets "inputs spent", and the
// lovelace is the adversary's. An output with an honest spend in flight
// is already in a mempool, which is first-come: racing it is front-run's
// job, sent ahead of the honest spend.
func planContention(s Sight, r *rand.Rand) (Step, string) {
	var idle []Locked
	for _, l := range s.Shared() {
		if !l.Busy {
			idle = append(idle, l)
		}
	}
	if len(idle) == 0 {
		return Step{}, "no shared output without a spend in flight"
	}
	l := idle[r.IntN(len(idle))]
	return Step{Attack: Contention, Spend: &l}, ""
}

func planMalformed(s Sight, r *rand.Rand) Step {
	ms := Mutations()
	m := ms[r.IntN(len(ms))]
	st := Step{Attack: Malformed, Mutation: m}
	if m != Steal {
		return st
	}
	vs := s.Victims()
	if len(vs) == 0 {
		st.Mutation = Truncate
		return st
	}
	v := vs[r.IntN(len(vs))]
	st.Victim = &v
	return st
}

func planRace(s Sight, r *rand.Rand) (Step, string) {
	payees := s.Payees()
	if len(payees) < 2 {
		return Step{}, "fewer than two payees"
	}
	k := min(2+r.IntN(2), len(payees))
	st := Step{Attack: DoubleSpend}
	for _, i := range r.Perm(len(payees))[:k] {
		st.Payees = append(st.Payees, Payee{Actor: payees[i]})
	}
	return st, ""
}
