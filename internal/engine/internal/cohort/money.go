package cohort

import (
	"iter"
	"math"
	"math/rand/v2"
	"strconv"

	"github.com/chrisguiney/cardano-continuum/internal/agenda"
	"github.com/chrisguiney/cardano-continuum/internal/check"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/wire"
	"github.com/chrisguiney/cardano-continuum/internal/household"
	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
	"github.com/chrisguiney/cardano-continuum/internal/store"
)

// Flight is the unfinished intents, which carry the open orders' holds.
type Flight interface {
	All() iter.Seq[*wire.Pending]
	// Spendable is the lovelace an owner can spend now.
	Spendable(id uint64) uint64
}

// Purse is a human's money: its budget, its fiat and its money clock.
type Purse struct {
	// Budget is the household drawn at the human's birth.
	Budget household.Budget
	// State is what the last month left: employment, disposable income
	// and bills.
	State household.State
	// Fiat is the human's fiat in cents, what open buys hold included.
	Fiat int64
	// Next is the human's next money step and Steps how many it has run.
	Next  sched.Time
	Steps uint64
	// Carry is the cents put aside from tranches too small to buy alone.
	Carry int64
	// h is the scheduled step.
	h sched.Handle
}

// Money is the humans' household money: each living human's money clock
// (internal/household), its fiat in cents and the holds of
// its open buy orders. A buy holds its fiat while it is open; the hold is
// the order's own Cents, so the snapshot's pay section restores it with
// the order (models/specs/fiat-escrow, rule 2), settling spends it and a
// release gives it back. Without humans or a market it does nothing.
type Money struct {
	// pp is the humans.
	pp *People
	// flight is the unfinished intents.
	flight Flight
	// step is the milliseconds between a human's money steps.
	step sched.Time
}

func newMoney(pp *People, f Flight) *Money {
	var step sched.Time
	if pp.On() {
		inv := pp.spec.Invest
		step = sched.Time(math.Round(pp.spec.Month * 1000 / float64(max(1, int(math.Round(1/inv.Every))))))
	}
	return &Money{pp: pp, flight: f, step: max(step, 1)}
}

// On reports whether humans have money: a humans section and a market.
func (mo *Money) On() bool { return mo.pp.On() && mo.pp.m.On() }

// stream is the human's stream named name and n.
func (mo *Money) stream(id population.HumanID, name string, n uint64) *rand.Rand {
	return mo.pp.seed.Derive("human", strconv.FormatUint(uint64(id), 10), name, strconv.FormatUint(n, 10)).Rand()
}

// Begin draws a newly founded human's budget and starting fiat in its own
// currency and schedules its first money step, at an offset into the first
// step drawn so humans born together do not all pay at once.
func (mo *Money) Begin(at sched.Time, h *Person) {
	if !mo.On() || h.Trader == nil {
		return
	}

	r := mo.stream(h.ID, "money", 0)
	usd := mo.pp.m.path.USD(h.Trader.Primary, at)
	b, fiat := household.Draw(mo.pp.spec.Tiers[h.Tier], mo.pp.spec.Invest, h.Motivation, usd, r)
	next := at + 1 + sched.Time(r.Float64()*float64(mo.step))
	h.Purse = &Purse{Budget: b, State: household.State{Employed: true}, Fiat: fiat, Next: next}
	h.Fiat0 = fiat
	h.Purse.h = mo.pp.r.env.Sched.Money(next, h.ID)
}

// Held is the cents the human's open buy orders hold.
func (mo *Money) Held(id population.HumanID) int64 {
	var held int64
	for p := range mo.flight.All() {
		if o := p.Order; o != nil && o.Human == uint64(id) && p.Kind == string(profile.IntentBuyADA) {
			held += o.Cents
		}
	}
	return held
}

// Available is the human's fiat less what its open buys hold.
func (mo *Money) Available(h *Person) int64 { return h.Purse.Fiat - mo.Held(h.ID) }

// Effect is what a money step asks the engine to start.
type Effect struct {
	// Buy is the cents to invest, into wallet Into.
	Buy  int64
	Into population.ActorID
	// Sell is the cents of an unpaid bill to raise by selling ADA from
	// wallet From.
	Sell int64
	From population.ActorID
	// Move, when not zero, is a savings wallet whose ADA moves to From
	// first, because From holds too little to sell.
	Move population.ActorID
	// Trend is the market's log return the purchase's style read.
	Trend float64
}

// Step runs human id's money step at at: a payday and the month's bills
// on the first step of each month, then a purchase when what the human
// has put aside buys at least least lovelace at the mid and fiat allows.
// It records the month and schedules the next step; a gone human's clock
// stops.
func (mo *Money) Step(at sched.Time, id population.HumanID, least uint64) Effect {
	h := mo.pp.Get(id)
	if h == nil || h.Purse == nil || h.Gone {
		return Effect{}
	}

	pu := h.Purse
	k := pu.Steps
	pu.Steps++
	pu.Next = at + mo.step
	pu.h = mo.pp.r.env.Sched.Money(pu.Next, id)

	var eff Effect
	if k%uint64(pu.Budget.Steps) == 0 {
		eff = mo.month(at, h, k)
	}

	eff.Trend = mo.pp.m.path.Trend(h.Trader.Primary, at)
	floor := household.Hold(least, mo.pp.m.path.Mid(h.Trader.Primary, at))
	eff.Buy, pu.Carry = household.Accrue(pu.Budget, pu.State, pu.Carry, mo.Available(h), floor, eff.Trend)
	if eff.Buy > 0 {
		eff.Into = mo.wallet(h, population.Savings, population.Spending, population.Trading, population.General)
	}

	if check.Slow {
		check.Post(mo.Available(h) >= 0, "cohort: Money.Step leaves a human's fiat below its holds")
	}
	return eff
}

// month is a payday and the month's bills.
func (mo *Money) month(at sched.Time, h *Person, k uint64) Effect {
	pu := h.Purse
	m := household.Month(pu.Budget, pu.State, mo.Available(h), mo.stream(h.ID, "month", k))
	pu.Fiat += m.Income - m.Paid
	pu.State = household.State{Employed: m.Employed, Disposable: m.Disposable, Bills: m.Due}

	emit := mo.pp.r.env.Emit
	if m.Switched {
		emit.Emit(at, wire.KindEmployment, 0, wire.Employment{Human: uint64(h.ID), Employed: m.Employed})
	}

	emit.Emit(at, wire.KindIncome, 0, wire.Income{Human: uint64(h.ID), Cents: m.Income, Currency: mo.pp.m.Code(h.Trader.Primary), Employed: m.Employed})
	emit.Emit(at, wire.KindExpense, 0, wire.Expense{Human: uint64(h.ID), Due: m.Due, Paid: m.Paid, Short: m.Short})
	if m.Short == 0 {
		return Effect{}
	}

	mo.frustrate(h, float64(m.Short)/float64(max(m.Due, 1)))
	from := mo.wallet(h, population.Spending, population.Trading, population.General)
	eff := Effect{Sell: m.Short, From: from}
	if save := mo.wallet(h, population.Savings); save != 0 && from != 0 && mo.flight.Spendable(uint64(from)) < mo.flight.Spendable(uint64(save)) {
		eff.Move = save
	}
	return eff
}

// frustrate raises the frustration of every wallet the human still uses
// by by: an unpaid bill makes the person likelier to give up.
func (mo *Money) frustrate(h *Person, by float64) {
	c := &mo.pp.r.census
	for _, id := range walletsOf(mo.pp.r, h.ID) {
		if a, _ := c.Get(id); a.State != population.Abandoned {
			_ = c.SetFrustration(id, a.Frustration+by)
		}
	}
}

// wallet is the human's first live wallet of the first purpose listed that
// it has one of; zero when none.
func (mo *Money) wallet(h *Person, purposes ...population.Purpose) population.ActorID {
	ids := walletsOf(mo.pp.r, h.ID)
	for _, p := range purposes {
		for _, id := range ids {
			if a, _ := mo.pp.r.census.Get(id); a.Purpose == p && a.State != population.Abandoned {
				return id
			}
		}
	}
	return 0
}

// Broke reports that the wallet's owner has too little fiat above its
// buffer to buy least lovelace at the mid: its agenda then scores no buy.
// Without money it is false.
func (mo *Money) Broke(id population.ActorID, at sched.Time, least uint64) bool {
	h := mo.pp.Of(id)
	if !mo.On() || h == nil || h.Purse == nil {
		return false
	}

	room := mo.Available(h) - int64(math.Ceil(h.Purse.Budget.Buffer*float64(h.Purse.State.Bills)))
	return room < household.Hold(least, mo.pp.m.path.Mid(h.Trader.Primary, at))
}

// Fund bounds a scored buy by its owner's fiat: an order holds the cents
// it costs, and a human keeps its buffer of bills, so a buy that would
// spend into the buffer shrinks to what is above it, down to min lovelace,
// and is refused below that. It marks the order with its owner and hold.
// It returns why the buy cannot go ahead; without money it does nothing.
func (mo *Money) Fund(p *wire.Pending, min uint64) string {
	o := p.Order
	if !mo.On() || o == nil || p.Kind != string(profile.IntentBuyADA) {
		return ""
	}

	h := mo.pp.Of(population.ActorID(p.To))
	if h == nil || h.Purse == nil {
		return ""
	}

	room := mo.Available(h) - int64(math.Ceil(h.Purse.Budget.Buffer*float64(h.Purse.State.Bills)))
	gross := p.Amount + o.Fee
	if household.Hold(gross, o.Primary) > room {
		gross = household.Lovelace(room, o.Primary)
	}

	if gross < min || gross <= o.Fee {
		return "no fiat above the buffer"
	}
	p.Amount, o.Human, o.Cents = gross-o.Fee, uint64(h.ID), household.Hold(gross, o.Primary)
	return ""
}

// Settled moves the owner's fiat when a trade's ADA is K deep: a buy spends
// what it held, a sale's proceeds arrive. It returns the human and cents
// for the Traded record, zero without one.
func (mo *Money) Settled(p *wire.Pending) (uint64, int64) {
	o := p.Order
	if !mo.On() || o == nil {
		return 0, 0
	}

	h := mo.pp.Of(population.ActorID(o.Wallet))
	if h == nil || h.Purse == nil {
		return 0, 0
	}

	if p.Kind == string(profile.IntentBuyADA) {
		h.Purse.Fiat -= o.Cents
		return uint64(h.ID), o.Cents
	}

	cents := household.Proceeds(p.Amount, o.Primary)
	h.Purse.Fiat += cents
	return uint64(h.ID), cents
}

// householdOf is the human's budget as HumanBorn records it, nil without
// money.
func householdOf(h *Person) *wire.Household {
	if h.Purse == nil {
		return nil
	}

	b := h.Purse.Budget
	return &wire.Household{
		Fiat:    h.Fiat0,
		Income:  b.Income,
		Ratio:   b.Ratio,
		Benefit: b.Benefit,
		Layoff:  b.Layoff,
		Hire:    b.Hire,
		Share:   b.Share,
		Steps:   b.Steps,
		Buffer:  b.Buffer,
		Style:   string(b.Style),
	}
}

// moneyOf is the human's money in the snapshot, nil without money.
func moneyOf(h *Person) *wire.MoneyRec {
	hh := householdOf(h)
	if hh == nil {
		return nil
	}

	pu := h.Purse
	return &wire.MoneyRec{
		Budget:     *hh,
		Fiat:       pu.Fiat,
		Employed:   pu.State.Employed,
		Disposable: pu.State.Disposable,
		Bills:      pu.State.Bills,
		Next:       pu.Next,
		Steps:      pu.Steps,
		Carry:      pu.Carry,
	}
}

// purseOf rebuilds a snapshot's money; its step is scheduled by Resume.
func purseOf(r *wire.MoneyRec) *Purse {
	if r == nil {
		return nil
	}

	b := r.Budget
	return &Purse{
		Budget: household.Budget{
			Income:  b.Income,
			Ratio:   b.Ratio,
			Benefit: b.Benefit,
			Layoff:  b.Layoff,
			Hire:    b.Hire,
			Share:   b.Share,
			Steps:   b.Steps,
			Buffer:  b.Buffer,
			Style:   household.Style(b.Style),
		},
		State: household.State{Employed: r.Employed, Disposable: r.Disposable, Bills: r.Bills},
		Fiat:  r.Fiat,
		Next:  r.Next,
		Steps: r.Steps,
		Carry: r.Carry,
	}
}

func fiat0(r *wire.MoneyRec) int64 {
	if r == nil {
		return 0
	}
	return r.Budget.Fiat
}

// Resume schedules every living human's next money step a snapshot
// recorded.
func (mo *Money) Resume() {
	for _, h := range mo.pp.list {
		if h.Purse != nil && !h.Gone {
			h.Purse.h = mo.pp.r.env.Sched.Money(h.Purse.Next, h.ID)
		}
	}
}

// Totals are the humans' fiat now and at their births, in cents summed
// over currencies: the fake summary checks that income, bills and trades
// account for the difference.
func (mo *Money) Totals() (now, start int64) {
	for _, h := range mo.pp.list {
		if h.Purse != nil {
			now += h.Purse.Fiat
			start += h.Fiat0
		}
	}
	return now, start
}

// owner is the wallet's owner as its identity row shows it, nil for an
// ownerless actor or without humans.
func (mo *Money) owner(a population.Actor) *store.Owner {
	pp := mo.pp
	h := pp.Get(a.Owner)
	if !pp.On() || h == nil {
		return nil
	}

	ids := walletsOf(pp.r, h.ID)
	ws := make([]uint64, len(ids))
	for i, id := range ids {
		ws[i] = uint64(id)
	}

	var money *store.Money
	if pu := h.Purse; pu != nil {
		b := pu.Budget
		money = &store.Money{
			Currency: pp.m.Code(h.Trader.Primary),
			Fiat:     pu.Fiat,
			Held:     mo.Held(h.ID),
			Fiat0:    h.Fiat0,
			Income:   b.Income,
			Ratio:    b.Ratio,
			Share:    b.Share,
			Steps:    b.Steps,
			Style:    string(b.Style),
			Buffer:   b.Buffer,
			Employed: pu.State.Employed,
		}
	}

	return &store.Owner{
		Human:      uint64(h.ID),
		Purpose:    string(a.Purpose),
		Motivation: string(h.Motivation),
		Tier:       pp.spec.Tiers[h.Tier].Name,
		Target:     h.Target,
		Spread:     h.Spread,
		Wallets:    ws,
		Gone:       h.Gone,
		Money:      money,
	}
}

// Note is the wallet's owner as a decision shows it, nil for an ownerless
// actor or without humans.
func (mo *Money) Note(a population.Actor) *agenda.OwnerNote {
	o := mo.owner(a)
	if o == nil {
		return nil
	}

	n := &agenda.OwnerNote{Human: o.Human, Motivation: o.Motivation, Purpose: o.Purpose, Tier: o.Tier}
	if m := o.Money; m != nil {
		n.Currency, n.Fiat, n.Held = m.Currency, m.Fiat, m.Held
	}
	return n
}
