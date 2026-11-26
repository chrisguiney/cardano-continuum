package operator

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// FactsOf reads the network parameters a phase is judged against from a
// shelley-genesis.json. Share is 1/len(staking.pools): the genesis gives
// every pool the same stake (internal/genesis PoolStake).
func FactsOf(shelley []byte) (Facts, error) {
	var g struct {
		ActiveSlotsCoeff float64 `json:"activeSlotsCoeff"`
		EpochLength      uint64  `json:"epochLength"`
		SecurityParam    uint64  `json:"securityParam"`
		SlotLength       float64 `json:"slotLength"`
		SystemStart      string  `json:"systemStart"`
		Staking          struct {
			Pools map[string]json.RawMessage `json:"pools"`
		} `json:"staking"`
	}
	if err := json.Unmarshal(shelley, &g); err != nil {
		return Facts{}, fmt.Errorf("decode shelley genesis: %w", err)
	}
	if g.SlotLength <= 0 {
		return Facts{}, errors.New("shelley genesis has no slotLength")
	}
	start, err := time.Parse(time.RFC3339, g.SystemStart)
	if err != nil {
		return Facts{}, fmt.Errorf("shelley genesis systemStart: %w", err)
	}
	f := Facts{
		Start: start, SlotLen: time.Duration(g.SlotLength * float64(time.Second)),
		F: g.ActiveSlotsCoeff, Epoch: g.EpochLength, K: g.SecurityParam,
	}
	if n := len(g.Staking.Pools); n > 0 {
		f.Share = 1 / float64(n)
	}
	return f, nil
}
