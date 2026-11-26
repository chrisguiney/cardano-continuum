package store

import (
	"encoding/json"
	"fmt"
)

// Token is a native asset balance. Policy and Name are hex.
type Token struct {
	Policy   string `json:"policy"`
	Name     string `json:"name"`
	NameText string `json:"name_text,omitempty"`
	Quantity uint64 `json:"quantity"`
}

// Identity is what the engine publishes about one actor in its row: public
// keys, addresses, traits and the engine's own view of its holdings and
// delegation as of the checkpoint that wrote it. It never holds key
// material; every field is derivable from public data.
type Identity struct {
	Kind   string `json:"kind"`
	Agenda string `json:"agenda"`

	PaymentAddr    string `json:"payment_addr,omitempty"`
	StakeAddr      string `json:"stake_addr,omitempty"`
	PaymentKeyHash string `json:"payment_key_hash,omitempty"`
	StakeKeyHash   string `json:"stake_key_hash,omitempty"`
	DRepID         string `json:"drep_id,omitempty"`
	PoolID         string `json:"pool_id,omitempty"`

	Competence   float64 `json:"competence"`
	Care         float64 `json:"care"`
	Patience     float64 `json:"patience"`
	LearningRate float64 `json:"learning_rate"`

	// Frustration and Hazard are the actor's state at the checkpoint;
	// Hazard is the per-second dormancy onset rate.
	Frustration float64 `json:"frustration"`
	Hazard      float64 `json:"hazard"`

	// Settled is the planner ledger's lovelace for the actor at the settled
	// point, in UTxOs outputs, of which Reserved is held by unfinished
	// payments. InFlight counts those payments and Unconfirmed the
	// lovelace they pay.
	Settled     uint64 `json:"settled"`
	UTxOs       int    `json:"utxos"`
	Reserved    uint64 `json:"reserved"`
	InFlight    int    `json:"in_flight"`
	Unconfirmed uint64 `json:"unconfirmed"`
	// Tokens are the native assets at the chain tip, which may include
	// blocks not yet settled.
	Tokens []Token `json:"tokens,omitempty"`

	StakeRegistered bool   `json:"stake_registered"`
	DelegatedPool   string `json:"delegated_pool,omitempty"`
	DelegatedActor  uint64 `json:"delegated_actor,omitempty"`
	DelegatedDRep   string `json:"delegated_drep,omitempty"`

	// Adversary accounting: lovelace spent across epochs against the
	// per-epoch ceiling, and the profile's success metric.
	CostSpent   uint64 `json:"cost_spent,omitempty"`
	CostCeiling uint64 `json:"cost_ceiling,omitempty"`
	Metric      string `json:"success_metric,omitempty"`
	// Budget and Attacks describe an adversary profile.
	Budget  uint64   `json:"budget,omitempty"`
	Attacks []string `json:"attacks,omitempty"`
}

// Encode returns the identity as the JSON a row carries.
func (i Identity) Encode() (string, error) {
	b, err := json.Marshal(i)
	if err != nil {
		return "", fmt.Errorf("encode identity: %w", err)
	}
	return string(b), nil
}

// DecodeIdentity parses a row's identity; an empty string is the zero
// Identity, as rows written before the column existed carry.
func DecodeIdentity(s string) (Identity, error) {
	var i Identity
	if s == "" {
		return i, nil
	}
	if err := json.Unmarshal([]byte(s), &i); err != nil {
		return Identity{}, fmt.Errorf("decode identity: %w", err)
	}
	return i, nil
}
