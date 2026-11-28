package people

import (
	"slices"

	"github.com/chrisguiney/cardano-continuum/internal/store"
)

// Row is one human in the People table. Money is in cents of Currency:
// Income is the monthly pay while employed and Bills the monthly bills it
// implies; Fiat what the human holds now and Held what its open buys hold
// of it. Lovelace is the ADA its wallets hold as the planner settled it.
type Row struct {
	ID         uint64  `json:"id"`
	Motivation string  `json:"motivation"`
	Tier       string  `json:"tier"`
	Currency   string  `json:"currency,omitempty"`
	Employed   bool    `json:"employed"`
	Income     int64   `json:"income"`
	Bills      int64   `json:"bills"`
	Fiat       int64   `json:"fiat"`
	Held       int64   `json:"held"`
	Share      float64 `json:"share"`
	Style      string  `json:"style,omitempty"`
	Wallets    int     `json:"wallets"`
	Lovelace   uint64  `json:"lovelace"`
	Purchases  int     `json:"purchases"`
	Invested   int64   `json:"invested"`
	Gone       bool    `json:"gone,omitempty"`
}

// Wallet is one wallet of a human: what it is for, its state and its ADA.
type Wallet struct {
	ID       uint64 `json:"id"`
	Purpose  string `json:"purpose"`
	Profile  string `json:"profile,omitempty"`
	State    string `json:"state,omitempty"`
	Lovelace uint64 `json:"lovelace"`
	Pool     string `json:"pool,omitempty"`
	DRep     string `json:"drep,omitempty"`
}

// Person is one human in full: its row, its strategy, its wallets, the
// totals the log holds and its recent money events.
type Person struct {
	Row
	Target     int      `json:"target"`
	Spread     float64  `json:"spread"`
	Buffer     float64  `json:"buffer"`
	Steps      int      `json:"steps"`
	Fiat0      int64    `json:"fiat0"`
	Months     int      `json:"months"`
	Out        int      `json:"out"`
	Short      int      `json:"short"`
	Paid       int64    `json:"paid"`
	Earned     int64    `json:"earned"`
	Bought     int64    `json:"bought"`
	Sold       int64    `json:"sold"`
	WalletList []Wallet `json:"wallet_list"`
	Events     []Event  `json:"events"`
}

// View is /api/people: every human, the fiat flows by currency, and the
// share of humans out of work at their last payday.
type View struct {
	Humans     []Row   `json:"humans"`
	Flows      []Flow  `json:"flows"`
	Unemployed float64 `json:"unemployed"`
}

// Wallets is what Build reads of each wallet: its identity row, profile
// and state.
type Wallets struct {
	Idents   map[uint64]store.Identity
	Profiles map[uint64]string
	States   map[uint64]string
}

// owners groups the identity rows by human: each human's owner as its
// first listed wallet's row shows it, and its wallets, ascending.
func (w Wallets) owners() map[uint64][]uint64 {
	out := map[uint64][]uint64{}
	for id, in := range w.Idents {
		if in.Owner != nil {
			out[in.Owner.Human] = append(out[in.Owner.Human], id)
		}
	}

	for _, ids := range out {
		slices.Sort(ids)
	}
	return out
}

// Build is the People view.
func Build(w Wallets, f *Fold) View {
	v := View{Humans: []Row{}, Flows: f.Flows()}
	out := 0
	for h, ids := range w.owners() {
		r := row(h, ids, w, f)
		v.Humans = append(v.Humans, r)
		if !r.Employed && !r.Gone {
			out++
		}
	}

	slices.SortFunc(v.Humans, func(a, b Row) int { return int(a.ID) - int(b.ID) })
	if n := len(v.Humans); n > 0 {
		v.Unemployed = float64(out) / float64(n)
	}
	return v
}

func row(h uint64, ids []uint64, w Wallets, f *Fold) Row {
	o := w.Idents[ids[0]].Owner
	t := f.get(h)
	r := Row{
		ID:         h,
		Motivation: o.Motivation,
		Tier:       o.Tier,
		Employed:   true,
		Wallets:    len(ids),
		Purchases:  t.purchases,
		Invested:   t.invested,
		Gone:       o.Gone || t.left,
	}
	for _, id := range ids {
		r.Lovelace += w.Idents[id].Settled
	}

	if m := o.Money; m != nil {
		r.Currency, r.Employed, r.Income = m.Currency, m.Employed, m.Income
		r.Bills = int64(float64(m.Income) * m.Ratio)
		r.Fiat, r.Held, r.Share, r.Style = m.Fiat, m.Held, m.Share, m.Style
	}
	return r
}

// Detail is one human, or false when no wallet names it.
func Detail(id uint64, w Wallets, f *Fold) (Person, bool) {
	ids, ok := w.owners()[id]
	if !ok {
		return Person{}, false
	}

	o := w.Idents[ids[0]].Owner
	t := f.get(id)
	p := Person{
		Row:    row(id, ids, w, f),
		Target: o.Target,
		Spread: o.Spread,
		Months: t.months,
		Out:    t.out,
		Short:  t.short,
		Paid:   t.paid,
		Earned: t.income,
		Bought: t.bought,
		Sold:   t.sold,
		Events: slices.Clone(t.events),
	}
	if m := o.Money; m != nil {
		p.Buffer, p.Steps, p.Fiat0 = m.Buffer, m.Steps, m.Fiat0
	}

	for _, wid := range ids {
		in := w.Idents[wid]
		p.WalletList = append(p.WalletList, Wallet{
			ID:       wid,
			Purpose:  in.Owner.Purpose,
			Profile:  w.Profiles[wid],
			State:    w.States[wid],
			Lovelace: in.Settled,
			Pool:     in.DelegatedPool,
			DRep:     in.DelegatedDRep,
		})
	}
	return p, true
}
