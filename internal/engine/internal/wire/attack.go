package wire

// KindAttack is the decision logging one attack step.
const KindAttack = "Attack"

// Attack is the body of an Attack decision: an adversary's step, charged
// Cost against its ceiling, sent as Txs (more than one for a double-spend
// race, all on the same inputs). Spend is the shared output a contention
// or front-run spends and Against the honest intent a front-run outbids;
// Payees are the actors paid, zero for the shared script address.
type Attack struct {
	Intent   uint64   `json:"intent"`
	Name     string   `json:"name"`
	Mutation string   `json:"mutation,omitempty"`
	Cost     uint64   `json:"cost"`
	Spend    *Ref     `json:"spend,omitzero"`
	Against  uint64   `json:"against,omitzero"`
	Payees   []uint64 `json:"payees,omitempty"`
	Txs      []TxID   `json:"txs"`
	// Value is the honest lovelace a Spend takes.
	Value uint64 `json:"value,omitzero"`
}

// Rival is a double-spend race's further transaction on the first one's
// inputs, sent right after it.
type Rival struct {
	ID TxID   `json:"id"`
	Tx []byte `json:"tx"`
}
