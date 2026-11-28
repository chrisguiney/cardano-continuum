package cohort

import (
	"math/rand/v2"

	"github.com/chrisguiney/cardano-continuum/internal/agenda"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/wire"
	"github.com/chrisguiney/cardano-continuum/internal/market"
	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
	"github.com/chrisguiney/cardano-continuum/internal/store"
)

// Desk is the wallets' side of the market: their positions, the orders
// they strike at their venues, and the fiat side of a trade once its ADA
// settles. It shares the Market's paths and exchanges.
type Desk struct {
	// m is the market the desk trades on.
	m *Market
}

// Position is the wallet's market position at t.
func (d *Desk) Position(id population.ActorID, t sched.Time) (agenda.Position, bool) {
	st := d.m.r.Rec(id)
	if !d.m.On() || st == nil || st.Trader == nil {
		return agenda.Position{}, false
	}
	tr := st.Trader
	return agenda.Position{
		Currency: d.m.Code(tr.Primary),
		Gain:     d.m.path.Mid(tr.Primary, t)/tr.Basis - 1,
		Trend:    d.m.path.Trend(tr.Primary, t),
	}, true
}

// Order quotes the wallet's buy or sale at its venue (market.Path.Venue)
// and names the exchange's actor. It draws from r.
func (d *Desk) Order(at sched.Time, id population.ActorID, buy bool, r *rand.Rand) (wire.Order, population.ActorID, bool) {
	pos, ok := d.Position(id, at)
	if !ok {
		return wire.Order{}, 0, false
	}
	tr := d.m.r.Rec(id).Trader
	v, ok := d.m.path.Venue(r, market.Order{Buy: buy, Primary: tr.Primary, Extra: tr.Extra, At: at, Last: tr.Venue})
	if !ok {
		return wire.Order{}, 0, false
	}
	x := d.m.path.Params().Exchanges[v.Exchange]
	o := wire.Order{
		Wallet:   uint64(id),
		Exchange: x.Name,
		Currency: d.m.Code(v.Currency),
		Rate:     v.Rate,
		Primary:  v.Primary,
		Gain:     pos.Gain,
	}
	if buy {
		o.Fee = x.Fee
	}
	return o, d.m.venues[v.Exchange], true
}

// Settled settles the fiat side of a trade whose ADA is K deep: a buy
// moves the wallet's basis, a sale realises its gain, and the wallet keeps
// the exchange for next time. It records the trade as a Traded decision.
func (d *Desk) Settled(at sched.Time, p *wire.Pending) {
	o := p.Order
	st := d.m.r.Rec(population.ActorID(o.Wallet))
	if st == nil || st.Trader == nil {
		return
	}
	tr := st.Trader
	t := wire.Traded{Intent: p.ID, Side: "sell", Exchange: o.Exchange, Currency: o.Currency, Lovelace: p.Amount, Rate: o.Rate}
	if p.Kind == string(profile.IntentBuyADA) {
		gross := p.Amount + o.Fee
		t.Side, t.Fee = "buy", o.Fee
		t.Fiat = float64(gross) / lovelace * o.Rate
		held := float64(tr.Units) / lovelace
		tr.Basis = (tr.Basis*held + float64(gross)/lovelace*o.Primary) / (held + float64(p.Amount)/lovelace)
		tr.Units += p.Amount
	} else {
		t.Fiat = float64(p.Amount) / lovelace * o.Rate
		t.Gain = o.Primary/tr.Basis - 1
		tr.Units -= min(tr.Units, p.Amount)
	}
	t.Basis = tr.Basis
	tr.Venue = venueOf(d.m.path.Params(), o.Exchange)
	d.m.r.env.Emit.Emit(at, wire.KindTraded, population.ActorID(o.Wallet), t)
}

// restore sets a wallet's basis, units and venue to a snapshot's.
func (d *Desk) restore(st *Actor, r wire.ActorRec) {
	if st.Trader == nil {
		return
	}
	st.Trader.Basis, st.Trader.Units, st.Trader.Venue = r.Basis, r.Units, venueOf(d.m.path.Params(), r.Venue)
}

// holding is a market wallet's identity row part, nil for other actors.
func (d *Desk) holding(id population.ActorID) *store.Market {
	st := d.m.r.Rec(id)
	if !d.m.On() || st == nil || st.Trader == nil {
		return nil
	}
	tr := st.Trader
	extra := make([]string, len(tr.Extra))
	for i, c := range tr.Extra {
		extra[i] = d.m.Code(c)
	}
	return &store.Market{
		Primary: d.m.Code(tr.Primary),
		Extra:   extra,
		Basis:   tr.Basis,
		Units:   tr.Units,
		Venue:   venueName(d.m.path.Params(), tr.Venue),
	}
}
