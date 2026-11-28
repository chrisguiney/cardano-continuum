package cohort

import (
	"math"
	"math/rand/v2"
	"slices"
	"strconv"
	"strings"

	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/wire"
	"github.com/chrisguiney/cardano-continuum/internal/grants"
	"github.com/chrisguiney/cardano-continuum/internal/market"
	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
	"github.com/chrisguiney/cardano-continuum/internal/rng"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

// Person is a human's in-memory record: what the census does not hold.
type Person struct {
	// ID is the human's identifier.
	ID population.HumanID
	// Motivation is the agenda the human's wallets score with.
	Motivation profile.AgendaName
	// Tier indexes the humans section's wealth tiers.
	Tier int
	// Target is how many wallets the human means to keep. New wallets of
	// its motivation join it until it has that many.
	Target int
	// Currency indexes the market's currencies, -1 until its first wallet
	// joins the market (or without one).
	Currency int
	// Spread is the owner's delegation spread in [0, 1]: how strongly its
	// wallets avoid the pools its other wallets are on.
	Spread float64
	// Trader is the human's place in the market, shared by every wallet it
	// owns: one currency, one cost basis over all its ADA. Nil without a
	// market.
	Trader *Trader
	// Gone marks a human whose every wallet is abandoned.
	Gone bool
}

// People is the humans who own the wallets. Without a humans section it
// is off: each wallet is its own owner (population.Census.Spawn) and People
// records nothing. With one, the fitted wallet streams still decide when a
// wallet appears; People decides whose it is. A new wallet joins a living
// human of the same motivation (and, for a birth in a market currency, the
// same currency) that has fewer wallets than its target, or founds a new
// human that draws its tier, target and spread.
type People struct {
	// spec is the humans section; nil when off.
	spec *profile.Humans
	// seed is the census root: humans' draws derive from it and their ID.
	seed rng.Seed
	// list is every human, list[id-1].
	list []*Person
	// proposer is the network proposer's profile name, which no person
	// owns.
	proposer string
	// r is the roster the wallets live in.
	r *Roster
	// m is the market, which names a human's currency.
	m *Market
	// owners are the owners a snapshot recorded by wallet, until the
	// wallets are revived.
	owners map[population.ActorID]population.Owner
}

func newPeople(sp Spec, r *Roster, m *Market) *People {
	return &People{spec: sp.Humans, seed: sp.Census, proposer: sp.Proposer, r: r, m: m}
}

// On reports whether the run has humans.
func (pp *People) On() bool { return pp.spec != nil }

// Get is the human with the given ID, nil when there is none.
func (pp *People) Get(id population.HumanID) *Person {
	if id == 0 || int(id) > len(pp.list) {
		return nil
	}
	return pp.list[id-1]
}

// Of is the human that owns the wallet, nil for an ownerless actor or
// without humans.
func (pp *People) Of(id population.ActorID) *Person {
	a, ok := pp.r.census.Get(id)
	if !ok || !pp.On() {
		return nil
	}
	return pp.Get(a.Owner)
}

// personal reports whether a wallet of profile p belongs to a person: a
// wallet that is not an exchange's, the network proposer's, a treasury
// recipient's or an adversary's.
func (pp *People) personal(p profile.Profile) bool {
	if p.Kind != profile.Wallet || p.Adversary != nil || p.Name == pp.proposer || strings.HasPrefix(p.Name, market.ProfilePrefix) {
		return false
	}

	_, recipient := grants.ByProfile(p.Name)
	return !recipient
}

// Place decides who owns the next actor, of profile p, born in market
// currency currency (-1 for none or drawn). founded is true when the actor
// founds a new human, which the call has added.
func (pp *People) Place(p profile.Profile, currency int) (o population.Owner, founded bool) {
	if !pp.On() || !pp.personal(p) {
		return population.Owner{}, false
	}

	next := population.ActorID(pp.r.census.Len() + 1)
	open := pp.open(p.Agenda, currency)
	if len(open) > 0 {
		r := pp.seed.Derive("place", strconv.FormatUint(uint64(next), 10)).Rand()
		h := open[r.IntN(len(open))]
		n := len(walletsOf(pp.r, h.ID))
		return population.Owner{Human: h.ID, Purpose: purposeFor(h.Motivation, n)}, false
	}

	h := pp.found(p.Agenda, currency)
	return population.Owner{Human: h.ID, Purpose: purposeFor(h.Motivation, 0)}, true
}

// open lists the living humans of the motivation that want another wallet,
// in ID order.
func (pp *People) open(motivation profile.AgendaName, currency int) []*Person {
	var out []*Person
	for _, h := range pp.list {
		if h.Gone || h.Motivation != motivation || len(walletsOf(pp.r, h.ID)) >= h.Target {
			continue
		}

		if currency >= 0 && h.Currency >= 0 && h.Currency != currency {
			continue
		}
		out = append(out, h)
	}
	return out
}

// walletsOf is every wallet the human ever owned, in spawn order.
func walletsOf(r *Roster, id population.HumanID) []population.ActorID {
	h, _ := r.census.Human(id)
	return h.Wallets
}

// found adds a human of the motivation and draws its tier, target and
// spread, each from its own stream.
func (pp *People) found(motivation profile.AgendaName, currency int) *Person {
	id := population.HumanID(len(pp.list) + 1)
	stream := func(name string) *rand.Rand {
		return pp.seed.Derive("human", strconv.FormatUint(uint64(id), 10), name).Rand()
	}

	tier := drawTier(pp.spec.Tiers, stream("tier"))
	h := &Person{
		ID:         id,
		Motivation: motivation,
		Tier:       tier,
		Target:     min(1+poisson(pp.spec.Tiers[tier].Wallets, stream("wallets")), pp.spec.MaxWallets),
		Currency:   currency,
		Spread:     min(max(pp.spec.Spread.Sample(stream("spread")), 0), 1),
	}
	pp.list = append(pp.list, h)
	return h
}

// drawTier picks a tier index by share.
func drawTier(ts []profile.Tier, r *rand.Rand) int {
	x := r.Float64()
	for i, t := range ts {
		if x < t.Share {
			return i
		}
		x -= t.Share
	}
	return len(ts) - 1
}

// poisson draws a Poisson count of mean mean (Knuth's method: the means
// here are a few at most).
func poisson(mean float64, r *rand.Rand) int {
	limit, p, k := math.Exp(-mean), 1.0, 0
	for {
		p *= r.Float64()
		if p <= limit {
			return k
		}
		k++
	}
}

// purposeOrder is, per motivation, the purpose of a human's first wallet,
// its second, and every one after. The first allows every intent the
// motivation scores, so a one-wallet human acts as the wallet did before
// humans; later wallets specialise.
var purposeOrder = map[profile.AgendaName][3]population.Purpose{
	"hodl":        {population.Spending, population.Savings, population.Savings},
	"yield":       {population.Spending, population.Savings, population.Staking},
	"profit":      {population.Spending, population.Savings, population.Trading},
	"civic":       {population.Governance, population.Savings, population.Governance},
	"dapp":        {population.Trading, population.Savings, population.Trading},
	"opportunist": {population.Trading, population.Savings, population.Spending},
}

// purposeFor is the purpose of a human's wallet number n (from 0).
func purposeFor(motivation profile.AgendaName, n int) population.Purpose {
	order, ok := purposeOrder[motivation]
	if !ok {
		return population.General
	}
	return order[min(n, 2)]
}

// Born records a human the wallet a just founded.
func (pp *People) Born(at sched.Time, a population.Actor) {
	h := pp.Get(a.Owner)
	var currency string
	if h.Trader != nil {
		currency = pp.m.Code(h.Trader.Primary)
	}

	pp.r.env.Emit.Emit(at, wire.KindHumanBorn, 0, wire.HumanBorn{
		Human:      uint64(h.ID),
		Motivation: string(h.Motivation),
		Tier:       pp.spec.Tiers[h.Tier].Name,
		Target:     h.Target,
		Currency:   currency,
		Spread:     h.Spread,
	})
}

// Abandoned notes that wallet a was abandoned: when it was its owner's
// last wallet, the human is gone, and a HumanLeft records it.
func (pp *People) Abandoned(at sched.Time, a population.Actor) {
	h := pp.Get(a.Owner)
	if h == nil || h.Gone {
		return
	}

	for _, id := range walletsOf(pp.r, h.ID) {
		if w, _ := pp.r.census.Get(id); w.State != population.Abandoned {
			return
		}
	}
	h.Gone = true
	pp.r.env.Emit.Emit(at, wire.KindHumanLeft, 0, wire.HumanLeft{Human: uint64(h.ID), Cause: "no_wallets"})
}

// Siblings are the other wallets of the actor's owner that are not
// abandoned, ascending; nil without one.
func (pp *People) Siblings(id population.ActorID) []population.ActorID {
	h := pp.Of(id)
	if h == nil {
		return nil
	}

	var out []population.ActorID
	for _, w := range walletsOf(pp.r, h.ID) {
		if a, _ := pp.r.census.Get(w); w != id && a.State != population.Abandoned {
			out = append(out, w)
		}
	}
	slices.Sort(out)
	return out
}

// Save is the humans' snapshot section, by ID.
func (pp *People) Save() []wire.HumanRec {
	out := make([]wire.HumanRec, 0, len(pp.list))
	for _, h := range pp.list {
		var ws []wire.OwnedRec
		for _, id := range walletsOf(pp.r, h.ID) {
			a, _ := pp.r.census.Get(id)
			ws = append(ws, wire.OwnedRec{ID: uint64(id), Purpose: string(a.Purpose)})
		}

		out = append(out, wire.HumanRec{
			Wallets:    ws,
			ID:         uint64(h.ID),
			Motivation: string(h.Motivation),
			Tier:       h.Tier,
			Target:     h.Target,
			Currency:   h.Currency,
			Spread:     h.Spread,
			Gone:       h.Gone,
		})
	}
	return out
}

// Load rebuilds the humans a Save recorded, before their wallets are
// revived.
func (pp *People) Load(rs []wire.HumanRec) {
	pp.owners = map[population.ActorID]population.Owner{}
	for _, r := range rs {
		for _, w := range r.Wallets {
			pp.owners[population.ActorID(w.ID)] = population.Owner{Human: population.HumanID(r.ID), Purpose: population.Purpose(w.Purpose)}
		}

		pp.list = append(pp.list, &Person{
			ID:         population.HumanID(r.ID),
			Motivation: profile.AgendaName(r.Motivation),
			Tier:       r.Tier,
			Target:     r.Target,
			Currency:   r.Currency,
			Spread:     r.Spread,
			Gone:       r.Gone,
		})
	}
}

// owner is the owner a snapshot recorded for the wallet; zero for an actor
// no person owns.
func (pp *People) owner(id population.ActorID) population.Owner { return pp.owners[id] }

// Holdings is how many wallets each human ever owned, by ID; nil when off.
func (pp *People) Holdings() []int {
	if !pp.On() {
		return nil
	}

	out := make([]int, len(pp.list))
	for i, h := range pp.list {
		out[i] = len(walletsOf(pp.r, h.ID))
	}
	return out
}
