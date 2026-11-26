package forger

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"

	"github.com/blinklabs-io/gouroboros/ledger/common"
)

// Params are the network constants a forger needs, read from the Shelley
// genesis. The chain is taken to be in Conway from slot 0, as
// internal/genesis renders it.
type Params struct {
	Magic       uint32
	SystemStart time.Time
	SlotLength  time.Duration
	// EpochLength is in slots.
	EpochLength   uint64
	SecurityParam uint64
	// ActiveSlots is f.
	ActiveSlots       *big.Rat
	SlotsPerKESPeriod uint64
	MaxKESEvolutions  uint64
	// MaxBodySize and MaxHeaderSize are the genesis protocol parameters.
	// A parameter update on chain is not followed.
	MaxBodySize, MaxHeaderSize uint64
	ProtoMajor, ProtoMinor     uint64
	// GenesisHash is the Shelley genesis hash, which is the chain's initial
	// nonce.
	GenesisHash [32]byte
	// PoolStake and TotalStake give the pool's relative stake. They come
	// from the genesis delegation and are not updated from the ledger.
	PoolStake, TotalStake uint64
}

// Epoch is the epoch slot falls in.
func (p Params) Epoch(slot uint64) uint64 { return slot / p.EpochLength }

// SlotAt is the slot in progress at t, and false before the system start.
func (p Params) SlotAt(t time.Time) (uint64, bool) {
	if t.Before(p.SystemStart) {
		return 0, false
	}
	return uint64(t.Sub(p.SystemStart) / p.SlotLength), true
}

// SlotStart is when slot begins.
func (p Params) SlotStart(slot uint64) time.Time {
	return p.SystemStart.Add(time.Duration(slot) * p.SlotLength)
}

func (p Params) check() error {
	switch {
	case p.EpochLength == 0 || p.SlotLength <= 0:
		return errors.New("epoch length and slot length must be positive")
	case p.ActiveSlots == nil || p.ActiveSlots.Sign() <= 0:
		return errors.New("active slots coefficient must be positive")
	case p.SlotsPerKESPeriod == 0 || p.MaxKESEvolutions == 0:
		return errors.New("slots per KES period and max KES evolutions must be positive")
	case p.TotalStake == 0 || p.PoolStake > p.TotalStake:
		return fmt.Errorf("pool stake %d of total %d is not a share", p.PoolStake, p.TotalStake)
	}
	return nil
}

// stabilityWindow is Conway's randomness stabilisation window, ceil(4k/f):
// a block later than this before the epoch's end no longer moves the
// candidate nonce (computeRandomnessStabilisationWindow, cardano-ledger
// Cardano/Ledger/BaseTypes.hs).
func (p Params) stabilityWindow() uint64 {
	n := new(big.Int).Mul(big.NewInt(4*int64(p.SecurityParam)), p.ActiveSlots.Denom())
	q, r := new(big.Int).QuoRem(n, p.ActiveSlots.Num(), new(big.Int))
	if r.Sign() != 0 {
		q.Add(q, big.NewInt(1))
	}
	return q.Uint64()
}

// File names in a genesis directory, as internal/genesis writes them.
const (
	nodeConfigFile = "config.json"
)

type nodeConfig struct {
	ShelleyGenesisFile string `json:"ShelleyGenesisFile"`
	ShelleyGenesisHash string `json:"ShelleyGenesisHash"`
}

type shelleyGenesis struct {
	ActiveSlotsCoeff  common.GenesisRat `json:"activeSlotsCoeff"`
	EpochLength       uint64            `json:"epochLength"`
	InitialFunds      map[string]uint64 `json:"initialFunds"`
	MaxKESEvolutions  uint64            `json:"maxKESEvolutions"`
	NetworkMagic      uint32            `json:"networkMagic"`
	SecurityParam     uint64            `json:"securityParam"`
	SlotLength        common.GenesisRat `json:"slotLength"`
	SlotsPerKESPeriod uint64            `json:"slotsPerKESPeriod"`
	SystemStart       time.Time         `json:"systemStart"`
	ProtocolParams    struct {
		MaxBlockBodySize   uint64 `json:"maxBlockBodySize"`
		MaxBlockHeaderSize uint64 `json:"maxBlockHeaderSize"`
		ProtocolVersion    struct {
			Major uint64 `json:"major"`
			Minor uint64 `json:"minor"`
		} `json:"protocolVersion"`
	} `json:"protocolParams"`
	Staking struct {
		// Stake maps a stake key hash to the pool it delegates to.
		Stake map[string]string `json:"stake"`
	} `json:"staking"`
}

// ErrGenesisMismatch is returned when the Shelley genesis file is not the
// one the node config pins. A forger on the wrong genesis would compute a
// wrong nonce and forge blocks invalid for a reason no scenario chose.
var ErrGenesisMismatch = errors.New("shelley genesis hash mismatch")

// LoadGenesis reads the node config and the Shelley genesis it names from
// dir, and returns the parameters with the stake of pool. It refuses a
// genesis file whose hash is not the one the config pins.
func LoadGenesis(dir string, pool [28]byte) (Params, error) {
	raw, err := os.ReadFile(filepath.Join(dir, nodeConfigFile))
	if err != nil {
		return Params{}, fmt.Errorf("read node config: %w", err)
	}
	var cfg nodeConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return Params{}, fmt.Errorf("parse node config %s: %w", nodeConfigFile, err)
	}
	if cfg.ShelleyGenesisFile == "" || filepath.Base(cfg.ShelleyGenesisFile) != cfg.ShelleyGenesisFile {
		return Params{}, fmt.Errorf("node config names shelley genesis %q: want a file name in the same directory", cfg.ShelleyGenesisFile)
	}
	file, err := os.ReadFile(filepath.Join(dir, cfg.ShelleyGenesisFile))
	if err != nil {
		return Params{}, fmt.Errorf("read shelley genesis: %w", err)
	}
	return ParseGenesis(file, cfg.ShelleyGenesisHash, pool)
}

// ParseGenesis builds Params from a Shelley genesis file whose Blake2b-256
// hash must be wantHash (hex).
func ParseGenesis(file []byte, wantHash string, pool [28]byte) (Params, error) {
	hash := common.Blake2b256Hash(file)
	if got := hex.EncodeToString(hash[:]); got != wantHash {
		return Params{}, fmt.Errorf("%w: file hashes to %s, config pins %s", ErrGenesisMismatch, got, wantHash)
	}
	var g shelleyGenesis
	if err := json.Unmarshal(file, &g); err != nil {
		return Params{}, fmt.Errorf("parse shelley genesis: %w", err)
	}
	if g.SlotLength.Rat == nil || g.ActiveSlotsCoeff.Rat == nil {
		return Params{}, errors.New("shelley genesis has no slotLength or activeSlotsCoeff")
	}
	ms := new(big.Rat).Mul(g.SlotLength.Rat, big.NewRat(1000, 1))
	if !ms.IsInt() || !ms.Num().IsInt64() {
		return Params{}, fmt.Errorf("slot length %s s is not a whole number of milliseconds", g.SlotLength.FloatString(6))
	}
	p := Params{
		Magic:             g.NetworkMagic,
		SystemStart:       g.SystemStart,
		SlotLength:        time.Duration(ms.Num().Int64()) * time.Millisecond,
		EpochLength:       g.EpochLength,
		SecurityParam:     g.SecurityParam,
		ActiveSlots:       new(big.Rat).Set(g.ActiveSlotsCoeff.Rat),
		SlotsPerKESPeriod: g.SlotsPerKESPeriod,
		MaxKESEvolutions:  g.MaxKESEvolutions,
		MaxBodySize:       g.ProtocolParams.MaxBlockBodySize,
		MaxHeaderSize:     g.ProtocolParams.MaxBlockHeaderSize,
		ProtoMajor:        g.ProtocolParams.ProtocolVersion.Major,
		ProtoMinor:        g.ProtocolParams.ProtocolVersion.Minor,
		GenesisHash:       hash,
	}
	var err error
	if p.PoolStake, p.TotalStake, err = genesisStake(g, pool); err != nil {
		return Params{}, err
	}
	if err := p.check(); err != nil {
		return Params{}, fmt.Errorf("shelley genesis: %w", err)
	}
	return p, nil
}

// genesisStake sums the initial funds whose stake credential delegates to
// a pool: the stake of pool, and of every pool.
func genesisStake(g shelleyGenesis, pool [28]byte) (own, total uint64, err error) {
	want := hex.EncodeToString(pool[:])
	for addrHex, lovelace := range g.InitialFunds {
		raw, err := hex.DecodeString(addrHex)
		if err != nil {
			return 0, 0, fmt.Errorf("initial funds address %q: %w", addrHex, err)
		}
		// A base address is a header byte, a 28-byte payment credential and
		// a 28-byte stake credential; other shapes carry no stake.
		if len(raw) != 57 || raw[0]>>4 > 3 {
			continue
		}
		to, ok := g.Staking.Stake[hex.EncodeToString(raw[29:])]
		if !ok {
			continue
		}
		total += lovelace
		if to == want {
			own += lovelace
		}
	}
	return own, total, nil
}
