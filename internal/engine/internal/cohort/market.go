package cohort

import (
	"strings"

	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/wire"
	"github.com/chrisguiney/cardano-continuum/internal/market"
	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

// Trader is a wallet's place in the ADA market. Currencies are indexes
// into the market's currencies.
type Trader struct {
	// Primary is the currency the wallet thinks in.
	Primary int
	// Extra are its other preferred currencies, drawn again from the
	// actor's own stream on a restore.
	Extra []int
	// Basis is the wallet's average price per ADA in its primary currency:
	// the mid when it was born, moved by every settled buy.
	Basis float64
	// Units is the lovelace Basis covers: its budget at birth, plus what it
	// bought, less what it sold. It stands in for the holdings a basis is
	// averaged over, so the basis needs no ledger read.
	Units uint64
	// Venue is the exchange it last traded on, or -1.
	Venue int
}

// Market is the cohort's ADA market: the price paths (internal/market),
// the actors that run the exchanges, each wallet's currencies, and the
// clock of the Quote records. Without a markets section it is off and
// every method does nothing.
type Market struct {
	// path is the market's price paths; nil when off.
	path *market.Path
	// venues are the exchanges' actors by exchange index, bound once the
	// genesis actors exist.
	venues []population.ActorID
	// at is the next Quote record's time.
	at sched.Time
	// r is the roster the exchanges' actors live in.
	r *Roster
	// people owns the wallets: a human's wallets share one Trader.
	people *People
}

func newMarket(sp Spec, r *Roster) *Market {
	m := &Market{r: r}
	if sp.Markets != nil {
		m.path = market.New(sp.Seed, *sp.Markets)
	}
	return m
}

// On reports whether the run has a market.
func (m *Market) On() bool { return m.path != nil }

// Path is the market's price paths; nil when off.
func (m *Market) Path() *market.Path { return m.path }

// Start queues the first Quote, one interval in: a quote at time zero
// would run before a restore could tell whether it had.
func (m *Market) Start() {
	if m.On() {
		m.schedule(0)
	}
}

// Save is the next Quote's time, the market's snapshot section.
func (m *Market) Save() sched.Time { return m.at }

// Load queues the Quote a snapshot saved.
func (m *Market) Load(at sched.Time) {
	if m.On() {
		m.at = at
		m.r.env.Sched.Quote(at)
	}
}

func (m *Market) schedule(now sched.Time) {
	every := sched.Time(m.path.Params().QuoteEvery * 1000)
	m.at = (now/every + 1) * every
	m.r.env.Sched.Quote(m.at)
}

// Quote records the market at at, then queues the next record.
func (m *Market) Quote(at sched.Time) {
	p := m.path.Params()
	q := wire.Quote{ADA: m.path.ADA(at)}
	for i, c := range p.Currencies {
		q.Mids = append(q.Mids, wire.Mid{Currency: c.Code, Mid: m.path.Mid(i, at)})
	}

	for xi, x := range p.Exchanges {
		for _, code := range x.Currencies {
			bid, ask, _ := m.path.Quote(xi, m.path.Currency(code), at)
			q.Venues = append(q.Venues, wire.VenueQuote{Exchange: x.Name, Currency: code, Bid: bid, Ask: ask})
		}
	}

	m.r.env.Emit.Emit(at, wire.KindQuote, 0, q)
	m.schedule(at)
}

// Bind finds the exchanges' actors among the roster's: each is the one
// actor of its exchange's profile (market.ProfileName).
func (m *Market) Bind() {
	if !m.On() {
		return
	}
	xs := m.path.Params().Exchanges
	m.venues = make([]population.ActorID, len(xs))
	for _, id := range m.r.IDs() {
		a, _ := m.r.Get(id)
		if x, ok := exchangeOf(m.path.Params(), a.Profile); ok && m.venues[x] == 0 {
			m.venues[x] = id
		}
	}
}

// exchangeOf is the exchange whose actor has the named profile.
func exchangeOf(ps profile.Markets, name string) (int, bool) {
	if !strings.HasPrefix(name, market.ProfilePrefix) {
		return 0, false
	}
	for i, x := range ps.Exchanges {
		if market.ProfileName(x.Name) == name {
			return i, true
		}
	}
	return 0, false
}

// Venue is exchange x's actor.
func (m *Market) Venue(x int) population.ActorID { return m.venues[x] }

// Exchange is the exchange actor id runs, if it runs one.
func (m *Market) Exchange(id population.ActorID) (int, bool) {
	for i, v := range m.venues {
		if v == id {
			return i, true
		}
	}
	return 0, false
}

// Join gives a newly registered wallet its currencies: primary when it is
// a currency index (a birth of that currency's stream, or a restore),
// otherwise drawn by share from the actor's own stream. The exchanges'
// actors and every other kind take none.
func (m *Market) Join(a population.Actor, st *Actor, primary int) {
	if !m.On() || a.Kind != profile.Wallet || strings.HasPrefix(a.Profile, market.ProfilePrefix) {
		return
	}
	h := m.people.Get(a.Owner)
	if h != nil && h.Trader != nil {
		m.share(a, st, h.Trader)
		return
	}

	if primary < 0 {
		primary = m.path.Primary(a.Stream("currency"))
	}
	st.Trader = &Trader{
		Primary: primary,
		Extra:   m.path.Extras(a.Stream("currencies"), primary),
		Basis:   m.path.Mid(primary, a.Born),
		Units:   uint64(st.Prof.Budget),
		Venue:   -1,
	}
	if h != nil && m.people.On() {
		h.Trader, h.Currency = st.Trader, primary
	}
}

// share makes a joining wallet trade as its owner, tr: its budget joins
// the owner's units at the mid of its birth.
func (m *Market) share(a population.Actor, st *Actor, tr *Trader) {
	add := float64(st.Prof.Budget) / lovelace
	held := float64(tr.Units) / lovelace
	if held+add > 0 {
		tr.Basis = (tr.Basis*held + m.path.Mid(tr.Primary, a.Born)*add) / (held + add)
	}
	tr.Units += uint64(st.Prof.Budget)
	st.Trader = tr
}

// deaths is the death factor an opted-in wallet's hazards thin against,
// nil for every other actor.
func (m *Market) deaths(st *Actor) sched.Modulation {
	if !m.On() || st.Trader == nil || !st.Prof.Hazards.Market {
		return nil
	}
	return m.path.Deaths(st.Trader.Primary, st.Prof.Agenda == "hodl")
}

// SellsOut reports that the actor, about to move to state to, sells its
// ADA first: an opted-in market wallet that abandons.
func (m *Market) SellsOut(id population.ActorID, to population.State) bool {
	st := m.r.Rec(id)
	return to == population.Abandoned && st != nil && m.deaths(st) != nil
}

// lovelace is the lovelace in one ADA.
const lovelace = 1_000_000

// venueOf is the index of the named exchange, or -1.
func venueOf(ps profile.Markets, name string) int {
	for i, x := range ps.Exchanges {
		if x.Name == name {
			return i
		}
	}
	return -1
}

// venueName is exchange x's name, empty for -1.
func venueName(ps profile.Markets, x int) string {
	if x < 0 {
		return ""
	}
	return ps.Exchanges[x].Name
}

// Code is the code of currency c.
func (m *Market) Code(c int) string { return m.path.Params().Currencies[c].Code }

// currencyOf is the index of the code a snapshot recorded, or -1.
func currencyOf(m *Market, code string) int {
	if !m.On() || code == "" {
		return -1
	}
	return m.path.Currency(code)
}
