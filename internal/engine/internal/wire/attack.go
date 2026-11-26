package wire

// KindAttack is the decision logging one attack step.
const KindAttack = "Attack"

// Attack is the body of an Attack decision: an adversary's step, charged
// Cost against its ceiling, sent as Txs (more than one for a double-spend
// race, all on the same inputs). Spend is the shared output a contention
// or front-run spends and Against the honest intent a front-run outbids;
// Payees are the actors paid, zero for the shared script address.
type Attack struct {
	Intent   uint64   `cbor:"1,keyasint"`
	Name     string   `cbor:"2,keyasint"`
	Mutation string   `cbor:"3,keyasint,omitempty"`
	Cost     uint64   `cbor:"4,keyasint"`
	Spend    *Ref     `cbor:"5,keyasint,omitempty"`
	Against  uint64   `cbor:"6,keyasint,omitempty"`
	Payees   []uint64 `cbor:"7,keyasint,omitempty"`
	Txs      []TxID   `cbor:"8,keyasint"`
	// Value is the honest lovelace a Spend takes.
	Value uint64 `cbor:"9,keyasint,omitempty"`
}

// Rival is a double-spend race's further transaction on the first one's
// inputs, sent right after it.
type Rival struct {
	ID TxID   `cbor:"1,keyasint"`
	Tx []byte `cbor:"2,keyasint"`
}
