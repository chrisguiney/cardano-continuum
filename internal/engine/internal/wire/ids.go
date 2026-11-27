package wire

import (
	"encoding/hex"
	"fmt"
)

// TxID is a transaction hash. It encodes as lowercase hex.
type TxID [32]byte

// MarshalText implements encoding.TextMarshaler.
func (id TxID) MarshalText() ([]byte, error) {
	return hex.AppendEncode(nil, id[:]), nil
}

// UnmarshalText implements encoding.TextUnmarshaler: exactly 64 hex digits.
func (id *TxID) UnmarshalText(b []byte) error {
	if len(b) != 2*len(id) {
		return fmt.Errorf("tx id: %d hex digits, want %d", len(b), 2*len(id))
	}
	if _, err := hex.Decode(id[:], b); err != nil {
		return fmt.Errorf("tx id: %w", err)
	}
	return nil
}

// Ref names a transaction output.
type Ref struct {
	Tx    TxID   `json:"tx"`
	Index uint32 `json:"index"`
}

// TxOut is an output as the engine reads it: who it pays and how much.
type TxOut struct {
	Index    uint32 `json:"index"`
	Address  []byte `json:"address"`
	Lovelace uint64 `json:"lovelace"`
	// Cbor is the output as the block carries it, with its assets and
	// datum; empty means lovelace only.
	Cbor []byte `json:"cbor,omitempty"`
}
