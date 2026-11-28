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
