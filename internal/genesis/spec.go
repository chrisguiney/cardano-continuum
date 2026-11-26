package genesis

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"
)

// Spec is everything a devnet genesis depends on besides the keystore.
type Spec struct {
	// Magic is the network magic, shared by Byron and Shelley.
	Magic uint32
	// SystemStart is slot 0. It is the only field left out of SpecHash, so
	// rerunning a Job later recognises the network it already made.
	SystemStart time.Time
	// EpochLength is in slots.
	EpochLength int64
	// SlotLength must be a whole number of milliseconds (Byron's unit).
	SlotLength time.Duration
	// SecurityParam is k.
	SecurityParam int64
	// ActiveSlotsCoeff is f; 1/f must be an integer.
	ActiveSlotsCoeff float64
	// MaxSupply is maxLovelaceSupply; what initial funds leave is reserves.
	MaxSupply uint64
	// Pools is the number of genesis pools, actors 0..Pools-1.
	Pools int
	// PoolStake is the lovelace each pool's owner holds, delegated to it.
	PoolStake uint64
	// FaucetLovelace funds keys.Store.Faucet at genesis.
	FaucetLovelace uint64
	// Committee is the number of constitutional committee members.
	Committee int
	// ProtocolVersion is the initial (major, minor).
	ProtocolVersion [2]uint
	// PoolHost is a fmt pattern taking the pool index, giving the host name
	// peers use to reach that pool on port PoolPort.
	PoolHost string
	// PoolPort is the node-to-node port of every pool.
	PoolPort int
	// TxExUnits and BlockExUnits are the Alonzo genesis maxTxExUnits and
	// maxBlockExUnits.
	TxExUnits, BlockExUnits ExUnits
	// CostModelLen truncates each language's cost model to that many
	// parameters, indexed by Language; zero keeps the full model for the
	// protocol version.
	CostModelLen [numLanguages]int
	// Allow lists the inconsistencies this devnet carries on purpose.
	Allow Allow
}

// Default is a Conway PV10 devnet with 500-slot epochs of 1 s slots, k=5,
// f=0.05, three pools, a three-member committee, mainnet's execution limits
// and full PV10 cost models.
func Default() Spec {
	return Spec{
		Magic:            42,
		EpochLength:      500,
		SlotLength:       time.Second,
		SecurityParam:    5,
		ActiveSlotsCoeff: 0.05,
		MaxSupply:        45_000_000_000_000_000,
		Pools:            3,
		PoolStake:        10_000_000_000_000,
		FaucetLovelace:   1_000_000_000_000_000,
		Committee:        3,
		ProtocolVersion:  [2]uint{10, 0},
		PoolHost:         "pool-%d",
		PoolPort:         3001,
		TxExUnits:        mainnetTxExUnits,
		BlockExUnits:     mainnetBlockExUnits,
	}
}

// conwayMajor is the first Conway protocol version.
const conwayMajor = 9

// Check reports the first reason s cannot make a working devnet.
//
// The epoch length must be a multiple of k/f with 1/f an integer: Amaru
// derives the epoch length as (1/f) * scale * k and has no other way to
// express it. It must also exceed 4k/f, the window after which the
// candidate nonce freezes; at or below it every epoch's nonce is fixed
// before the epoch starts.
//
// The execution limits and cost models must be consistent (checkLimits,
// checkCostModels) unless s.Allow names the inconsistency.
func Check(s Spec) error {
	if err := checkFields(s); err != nil {
		return err
	}
	if err := checkLimits(s); err != nil {
		return err
	}
	if err := checkCostModels(s); err != nil {
		return err
	}
	inv := 1 / s.ActiveSlotsCoeff
	if math.Abs(inv-math.Round(inv)) > 1e-9 {
		return fmt.Errorf("activeSlotsCoeff %v: 1/f = %v is not an integer", s.ActiveSlotsCoeff, inv)
	}
	unit := s.SecurityParam * int64(math.Round(inv))
	if s.EpochLength%unit != 0 {
		return fmt.Errorf("epochLength %d is not a multiple of k/f = %d", s.EpochLength, unit)
	}
	if s.EpochLength <= 4*unit {
		return fmt.Errorf("epochLength %d must exceed the stability window 4k/f = %d", s.EpochLength, 4*unit)
	}
	return nil
}

func checkFields(s Spec) error {
	switch {
	case s.Magic == 0:
		return errors.New("network magic must be non-zero")
	case s.SecurityParam <= 0:
		return fmt.Errorf("securityParam %d must be positive", s.SecurityParam)
	case !(s.ActiveSlotsCoeff > 0 && s.ActiveSlotsCoeff <= 1):
		return fmt.Errorf("activeSlotsCoeff %v must be in (0, 1]", s.ActiveSlotsCoeff)
	case s.EpochLength <= 0:
		return fmt.Errorf("epochLength %d must be positive", s.EpochLength)
	case s.SlotLength < time.Millisecond || s.SlotLength%time.Millisecond != 0:
		return fmt.Errorf("slotLength %s must be a positive whole number of milliseconds", s.SlotLength)
	case s.Pools < 1:
		return fmt.Errorf("pools %d: need at least one genesis pool", s.Pools)
	case s.Committee < 1:
		return fmt.Errorf("committee %d: Conway governance needs at least one member", s.Committee)
	case s.ProtocolVersion[0] < conwayMajor:
		return fmt.Errorf("protocol version %d.%d is before Conway", s.ProtocolVersion[0], s.ProtocolVersion[1])
	case s.PoolPort <= 0 || s.PoolPort > 65535:
		return fmt.Errorf("pool port %d out of range", s.PoolPort)
	}
	staked := uint64(s.Pools) * s.PoolStake
	if staked/uint64(s.Pools) != s.PoolStake || staked+s.FaucetLovelace < staked ||
		staked+s.FaucetLovelace > s.MaxSupply {
		return fmt.Errorf("maxSupply %d is below pool stake %d x %d plus faucet %d",
			s.MaxSupply, s.Pools, s.PoolStake, s.FaucetLovelace)
	}
	return nil
}

// specHash fingerprints s without SystemStart.
func specHash(s Spec) string {
	s.SystemStart = time.Time{}
	b, err := json.Marshal(s)
	if err != nil {
		// Unreachable: Spec holds only plain values.
		panic("genesis: marshal spec: " + err.Error())
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
