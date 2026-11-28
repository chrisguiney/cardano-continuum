package wire

import "github.com/chrisguiney/cardano-continuum/internal/sched"

// HumanBorn is the body of a HumanBorn decision: a human founded by its
// first wallet. Humans exist only in a run with a humans section; the
// record's actor is zero and Human names the person.
type HumanBorn struct {
	Human      uint64 `json:"human"`
	Motivation string `json:"motivation"`
	Tier       string `json:"tier"`
	// Target is how many wallets the human means to keep.
	Target int `json:"target"`
	// Currency is the human's currency when the run has a market.
	Currency string  `json:"currency,omitempty"`
	Spread   float64 `json:"spread"`
	// Money is the human's household, without a market none.
	Money *Household `json:"money,omitempty"`
}

// Household is a human's drawn budget: Fiat is the cents it starts with,
// Income its monthly pay in cents while employed, Ratio its monthly bills
// over its pay, Share the part of what is left it invests every Steps
// purchases a month, timed by Style.
type Household struct {
	Fiat    int64   `json:"fiat"`
	Income  int64   `json:"income"`
	Ratio   float64 `json:"ratio"`
	Benefit float64 `json:"benefit"`
	Layoff  float64 `json:"layoff"`
	Hire    float64 `json:"hire"`
	Share   float64 `json:"share"`
	Steps   int     `json:"steps"`
	Buffer  float64 `json:"buffer"`
	Style   string  `json:"style"`
}

// Income is the body of an Income decision: a human's payday, in cents of
// its currency.
type Income struct {
	Human    uint64 `json:"human"`
	Cents    int64  `json:"cents"`
	Currency string `json:"currency"`
	Employed bool   `json:"employed"`
}

// Expense is the body of an Expense decision: a month's bills, what the
// available fiat paid and what it could not (fiat never goes negative).
type Expense struct {
	Human uint64 `json:"human"`
	Due   int64  `json:"due"`
	Paid  int64  `json:"paid"`
	Short int64  `json:"short,omitzero"`
}

// Employment is the body of an Employment decision: a human lost or found
// a job.
type Employment struct {
	Human    uint64 `json:"human"`
	Employed bool   `json:"employed"`
}

// Invested is the body of an Invested decision: a human's purchase of ADA
// from its income into one of its wallets, recorded once the buy Intent is
// submitted; once settled it has a Traded record. Trend is the market's log
// return the style read.
type Invested struct {
	Intent uint64  `json:"intent"`
	Human  uint64  `json:"human"`
	Wallet uint64  `json:"wallet"`
	Cents  int64   `json:"cents"`
	Style  string  `json:"style"`
	Trend  float64 `json:"trend"`
}

// HumanLeft is the body of a HumanLeft decision: a human with no wallet
// left. Cause says why.
type HumanLeft struct {
	Human uint64 `json:"human"`
	Cause string `json:"cause"`
}

// HumanRec is one human in the snapshot.
type HumanRec struct {
	ID         uint64 `json:"id"`
	Motivation string `json:"motivation"`
	// Tier indexes the humans section's tiers.
	Tier   int `json:"tier"`
	Target int `json:"target"`
	// Currency indexes the market's currencies, -1 for none.
	Currency int     `json:"currency"`
	Spread   float64 `json:"spread"`
	Gone     bool    `json:"gone,omitzero"`
	// Wallets are the human's wallets in spawn order.
	Wallets []OwnedRec `json:"wallets"`
	// Money is the household's budget and state; nil without a market.
	Money *MoneyRec `json:"money,omitempty"`
}

// MoneyRec is a human's money in the snapshot: its budget, its fiat in
// cents (what open buys hold is not stored: it is their orders' Cents),
// its last month and its money clock.
type MoneyRec struct {
	Budget     Household  `json:"budget"`
	Fiat       int64      `json:"fiat"`
	Employed   bool       `json:"employed"`
	Disposable int64      `json:"disposable"`
	Bills      int64      `json:"bills"`
	Next       sched.Time `json:"next"`
	Steps      uint64     `json:"steps"`
	// Carry is the cents put aside from tranches too small to buy alone.
	Carry int64 `json:"carry,omitzero"`
}

// OwnedRec is one wallet of a human in the snapshot and what it is for.
type OwnedRec struct {
	ID      uint64 `json:"id"`
	Purpose string `json:"purpose"`
}
