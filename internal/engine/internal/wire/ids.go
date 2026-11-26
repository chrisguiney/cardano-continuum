package wire

// TxID is a transaction hash.
type TxID [32]byte

// Ref names a transaction output.
type Ref struct {
	Tx    TxID   `cbor:"1,keyasint"`
	Index uint32 `cbor:"2,keyasint"`
}

// TxOut is an output as the engine reads it: who it pays and how much.
type TxOut struct {
	Index    uint32 `cbor:"1,keyasint"`
	Address  []byte `cbor:"2,keyasint"`
	Lovelace uint64 `cbor:"3,keyasint"`
	// Cbor is the output as the block carries it, with its assets and
	// datum; empty means lovelace only.
	Cbor []byte `cbor:"4,keyasint,omitempty"`
}
