package chain

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"slices"
	"time"
)

// Guard errors.
var (
	// ErrPublicNetwork: the network magic is mainnet, preprod, preview or
	// sanchonet. The engine floods its network with deliberate mistakes, so
	// it refuses these whatever else matches.
	ErrPublicNetwork = errors.New("chain: refusing a public network")
	// ErrGenesisMismatch: the node runs a different genesis than the run's.
	ErrGenesisMismatch = errors.New("chain: node genesis differs from the run's")
)

// publicMagics are mainnet, preprod, preview and sanchonet.
var publicMagics = []uint32{764824073, 1, 2, 4}

// Genesis is what identifies a devnet over local-state-query. The query
// returns the decoded Shelley genesis, not the file, so the file hash
// cannot be compared; these fields are the ones a rerun with another
// system start or another spec would change.
type Genesis struct {
	Magic         uint32
	Start         time.Time
	EpochLength   uint64
	SecurityParam uint64
	SlotLength    time.Duration
	// ActiveSlots is the active slot coefficient f; zero when unknown.
	// Guard does not compare it: the query does not return it.
	ActiveSlots float64
}

// StabilityWindow is 3k/f slots: how far past its tip a node can translate
// slots to time, so the latest validity bound a script transaction may
// carry without the node answering PastHorizon. Zero when f is unknown.
func (g Genesis) StabilityWindow() uint64 {
	if g.ActiveSlots <= 0 || g.ActiveSlots > 1 {
		return 0
	}
	return uint64(3 * float64(g.SecurityParam) / g.ActiveSlots)
}

// Guard refuses a public network, then any node whose genesis differs
// from want.
func Guard(want, got Genesis) error {
	for _, g := range []Genesis{want, got} {
		if slices.Contains(publicMagics, g.Magic) {
			return fmt.Errorf("%w: magic %d", ErrPublicNetwork, g.Magic)
		}
	}
	if !want.Start.Equal(got.Start) || want.Magic != got.Magic || want.EpochLength != got.EpochLength ||
		want.SecurityParam != got.SecurityParam || want.SlotLength != got.SlotLength {
		return fmt.Errorf("%w: want %+v, node has %+v", ErrGenesisMismatch, want, got)
	}
	return nil
}

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
		ActiveSlots   float64     `json:"activeSlotsCoeff"`
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
		SecurityParam: doc.SecurityParam, SlotLength: time.Duration(ns.Num().Int64()), ActiveSlots: doc.ActiveSlots,
	}, nil
}
