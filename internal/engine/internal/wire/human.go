package wire

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
}

// OwnedRec is one wallet of a human in the snapshot and what it is for.
type OwnedRec struct {
	ID      uint64 `json:"id"`
	Purpose string `json:"purpose"`
}
