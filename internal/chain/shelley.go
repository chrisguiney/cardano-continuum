package chain

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"time"
)

// maxGenesisFile bounds the Shelley genesis file a caller should read; the
// devnet's is a few kilobytes.
const maxGenesisFile = 1 << 20

// ParseShelleyGenesis reads a Shelley genesis file's identity: the fields
// Guard compares against a node's local-state-query answer. Field names are
// cardano-ledger's ToJSON ShelleyGenesis (Cardano/Ledger/Shelley/Genesis.hs).
func ParseShelleyGenesis(raw []byte) (Genesis, error) {
	if len(raw) > maxGenesisFile {
		return Genesis{}, fmt.Errorf("chain: shelley genesis of %d bytes exceeds %d", len(raw), maxGenesisFile)
	}
	var doc struct {
		NetworkMagic  uint32      `json:"networkMagic"`
		SystemStart   time.Time   `json:"systemStart"`
		EpochLength   uint64      `json:"epochLength"`
		SecurityParam uint64      `json:"securityParam"`
		SlotLength    json.Number `json:"slotLength"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return Genesis{}, fmt.Errorf("chain: shelley genesis: %w", err)
	}
	if doc.SystemStart.IsZero() || doc.EpochLength == 0 || doc.SecurityParam == 0 {
		return Genesis{}, errors.New("chain: shelley genesis lacks systemStart, epochLength or securityParam")
	}
	// slotLength is decimal seconds; a big.Rat keeps 0.2 exact.
	secs, ok := new(big.Rat).SetString(doc.SlotLength.String())
	if !ok || secs.Sign() <= 0 {
		return Genesis{}, fmt.Errorf("chain: shelley genesis slotLength %q", doc.SlotLength)
	}
	ns := new(big.Rat).Mul(secs, big.NewRat(int64(time.Second), 1))
	if !ns.IsInt() {
		return Genesis{}, fmt.Errorf("chain: shelley genesis slotLength %q is not whole nanoseconds", doc.SlotLength)
	}
	return Genesis{
		Magic: doc.NetworkMagic, Start: doc.SystemStart.UTC(), EpochLength: doc.EpochLength,
		SecurityParam: doc.SecurityParam, SlotLength: time.Duration(ns.Num().Int64()),
	}, nil
}
