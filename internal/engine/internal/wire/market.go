package wire

// Quote is the body of a Quote decision: the market at the record's time.
// ADA is ADA/USD; Mids are each currency's mid rate and Venues each
// exchange's quotes, in units of the currency per ADA.
type Quote struct {
	ADA    float64      `json:"ada"`
	Mids   []Mid        `json:"mids"`
	Venues []VenueQuote `json:"venues"`
}

// Mid is one currency's mid rate.
type Mid struct {
	Currency string  `json:"currency"`
	Mid      float64 `json:"mid"`
}

// VenueQuote is one exchange's bid and ask in one currency.
type VenueQuote struct {
	Exchange string  `json:"exchange"`
	Currency string  `json:"currency"`
	Bid      float64 `json:"bid"`
	Ask      float64 `json:"ask"`
}

// Order is an exchange trade's quote, struck when the wallet chose it. A
// buy's pending intent is the exchange's payment to Wallet, a sale's the
// wallet's payment to the exchange; Rate is the exchange's ask or bid in
// Currency per ADA, Primary the price in the wallet's primary currency,
// and Fee the lovelace a buy's withdrawal keeps back. Gain is the wallet's
// mid over its cost basis, minus one, when it ordered.
type Order struct {
	Wallet   uint64  `json:"wallet"`
	Exchange string  `json:"exchange"`
	Currency string  `json:"currency"`
	Rate     float64 `json:"rate"`
	Primary  float64 `json:"primary"`
	Fee      uint64  `json:"fee,omitzero"`
	Gain     float64 `json:"gain"`
}

// Traded is the body of a Traded decision: an order's ADA settled K deep,
// so the exchange settles its fiat side. Side is "buy" or "sell"; Lovelace
// is what moved on chain, Fiat the currency units paid or received. Basis
// is the wallet's cost basis per ADA in its primary currency afterwards;
// Gain a sale's price over the basis before it, minus one.
type Traded struct {
	Intent   uint64  `json:"intent"`
	Side     string  `json:"side"`
	Exchange string  `json:"exchange"`
	Currency string  `json:"currency"`
	Lovelace uint64  `json:"lovelace"`
	Fee      uint64  `json:"fee,omitzero"`
	Fiat     float64 `json:"fiat"`
	Rate     float64 `json:"rate"`
	Basis    float64 `json:"basis"`
	Gain     float64 `json:"gain,omitzero"`
}
