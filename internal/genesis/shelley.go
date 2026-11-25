package genesis

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/chrisguiney/cardano-continuum/internal/keys"
)

// The struct order is the key order in the file. Field names follow
// cardano-ledger's ToJSON ShelleyGenesis (Cardano/Ledger/Shelley/Genesis.hs).
type shelleyDoc struct {
	ActiveSlotsCoeff  json.Number       `json:"activeSlotsCoeff"`
	EpochLength       int64             `json:"epochLength"`
	GenDelegs         struct{}          `json:"genDelegs"`
	InitialFunds      map[string]uint64 `json:"initialFunds"`
	MaxKESEvolutions  int               `json:"maxKESEvolutions"`
	MaxLovelaceSupply uint64            `json:"maxLovelaceSupply"`
	NetworkID         string            `json:"networkId"`
	NetworkMagic      uint32            `json:"networkMagic"`
	ProtocolParams    shelleyParams     `json:"protocolParams"`
	SecurityParam     int64             `json:"securityParam"`
	SlotLength        json.Number       `json:"slotLength"`
	SlotsPerKESPeriod int               `json:"slotsPerKESPeriod"`
	Staking           shelleyStaking    `json:"staking"`
	SystemStart       string            `json:"systemStart"`
	UpdateQuorum      int               `json:"updateQuorum"`
}

// shelleyParams are the initial protocol parameters. They carry through the
// epoch-0 hard forks into Conway. d=0 hands every slot to the stake pools;
// with d=1 the absent genesis delegates would own them and nothing would
// forge. Deposits and fees are mainnet's; minPoolCost is 0 so the cost-0
// genesis pools are valid.
type shelleyParams struct {
	A0               json.Number `json:"a0"`
	Decentralisation json.Number `json:"decentralisationParam"`
	EMax             int         `json:"eMax"`
	ExtraEntropy     struct {
		Tag string `json:"tag"`
	} `json:"extraEntropy"`
	KeyDeposit         uint64 `json:"keyDeposit"`
	MaxBlockBodySize   int    `json:"maxBlockBodySize"`
	MaxBlockHeaderSize int    `json:"maxBlockHeaderSize"`
	MaxTxSize          int    `json:"maxTxSize"`
	MinFeeA            int    `json:"minFeeA"`
	MinFeeB            int    `json:"minFeeB"`
	MinPoolCost        uint64 `json:"minPoolCost"`
	MinUTxOValue       uint64 `json:"minUTxOValue"`
	NOpt               int    `json:"nOpt"`
	PoolDeposit        uint64 `json:"poolDeposit"`
	ProtocolVersion    struct {
		Major uint `json:"major"`
		Minor uint `json:"minor"`
	} `json:"protocolVersion"`
	Rho json.Number `json:"rho"`
	Tau json.Number `json:"tau"`
}

type shelleyStaking struct {
	Pools map[string]shelleyPool `json:"pools"`
	// Stake maps a stake key hash to the pool it delegates to.
	Stake map[string]string `json:"stake"`
}

type shelleyPool struct {
	Cost          uint64        `json:"cost"`
	Margin        json.Number   `json:"margin"`
	Metadata      *struct{}     `json:"metadata"`
	Owners        []string      `json:"owners"`
	Pledge        uint64        `json:"pledge"`
	PublicKey     string        `json:"publicKey"`
	Relays        []struct{}    `json:"relays"`
	RewardAccount rewardAccount `json:"rewardAccount"`
	Vrf           string        `json:"vrf"`
}

type rewardAccount struct {
	Credential struct {
		KeyHash string `json:"keyHash"`
	} `json:"credential"`
	Network string `json:"network"`
}

// stakedPool is one genesis pool with the owner whose funds back it.
type stakedPool struct {
	keys  keys.PoolKeys
	owner keys.Wallet
}

func renderShelley(s Spec, faucet keys.Wallet, pools []stakedPool) ([]byte, error) {
	funds := map[string]uint64{}
	if err := addFunds(funds, faucet, s.FaucetLovelace); err != nil {
		return nil, err
	}
	staking := shelleyStaking{Pools: map[string]shelleyPool{}, Stake: map[string]string{}}
	for i, p := range pools {
		if err := addFunds(funds, p.owner, s.PoolStake); err != nil {
			return nil, fmt.Errorf("pool %d owner: %w", i, err)
		}
		id := hex.EncodeToString(p.keys.PoolID[:])
		stakeKey := p.owner.StakeKeyHash()
		owner := hex.EncodeToString(stakeKey[:])
		reg := shelleyPool{
			Margin:    "0",
			Owners:    []string{owner},
			PublicKey: id,
			Relays:    []struct{}{},
			Vrf:       hex.EncodeToString(p.keys.VRFVKeyHash[:]),
		}
		reg.RewardAccount.Credential.KeyHash = owner
		reg.RewardAccount.Network = "Testnet"
		staking.Pools[id] = reg
		staking.Stake[owner] = id
	}

	d := shelleyDoc{
		ActiveSlotsCoeff:  number(s.ActiveSlotsCoeff),
		EpochLength:       s.EpochLength,
		InitialFunds:      funds,
		MaxKESEvolutions:  60,
		MaxLovelaceSupply: s.MaxSupply,
		NetworkID:         "Testnet",
		NetworkMagic:      s.Magic,
		ProtocolParams:    defaultParams(s.ProtocolVersion),
		SecurityParam:     s.SecurityParam,
		SlotLength:        number(s.SlotLength.Seconds()),
		SlotsPerKESPeriod: 129600,
		Staking:           staking,
		SystemStart:       s.SystemStart.UTC().Format(time.RFC3339),
		UpdateQuorum:      1,
	}
	return marshal(d)
}

func defaultParams(pv [2]uint) shelleyParams {
	p := shelleyParams{
		A0:                 "0.3",
		Decentralisation:   "0",
		EMax:               18,
		KeyDeposit:         2_000_000,
		MaxBlockBodySize:   90112,
		MaxBlockHeaderSize: 1100,
		MaxTxSize:          16384,
		MinFeeA:            44,
		MinFeeB:            155381,
		NOpt:               500,
		PoolDeposit:        500_000_000,
		Rho:                "0.003",
		Tau:                "0.2",
	}
	p.ExtraEntropy.Tag = "NeutralNonce"
	p.ProtocolVersion.Major, p.ProtocolVersion.Minor = pv[0], pv[1]
	return p
}

func addFunds(funds map[string]uint64, w keys.Wallet, lovelace uint64) error {
	raw, err := w.Address().Bytes()
	if err != nil {
		return fmt.Errorf("address bytes: %w", err)
	}
	funds[hex.EncodeToString(raw)] += lovelace
	return nil
}

// number writes f in the shortest decimal form that reads back as f, never
// in E notation (0.05, not 5e-02).
func number(f float64) json.Number {
	return json.Number(strconv.FormatFloat(f, 'f', -1, 64))
}
