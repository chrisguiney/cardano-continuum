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
// material; every field is derivable from public data. The groups are
// embedded, so the row's JSON is one flat object.
type Identity struct {
	// Kind is the actor kind the UI names (wallet, pool, adversary, ...).
	Kind string `json:"kind"`
	// Agenda is the profile's agenda.
	Agenda string `json:"agenda"`
	// Keys are the actor's addresses and key hashes.
	Keys
	// Traits are the actor's traits and state at the checkpoint.
	Traits
	// Holdings are the planner ledger's view of the actor's funds.
	Holdings
	// Delegation is the actor's stake registration and delegations.
	Delegation
	// Adversary is an adversary profile's accounting; zero otherwise.
	Adversary
	// Market is a market wallet's currencies and cost basis; nil for every
	// other actor.
	Market *Market `json:"market,omitempty"`
	// Owner is the human that owns the wallet, in a run with humans; nil
	// otherwise.
	Owner *Owner `json:"owner,omitempty"`
}

// Owner is the human behind a wallet as of the checkpoint: who it is, what
// the wallet is for to it, and the household money it holds off chain.
// Amounts are cents of the human's currency.
type Owner struct {
	// Human is the owner's ID and Purpose what the wallet is for.
	Human   uint64 `json:"human"`
	Purpose string `json:"purpose"`
	// Motivation is the agenda the owner's wallets score with; Tier its
	// wealth tier; Target the wallets it means to keep; Spread how far it
	// spreads its wallets' delegations.
	Motivation string  `json:"motivation"`
	Tier       string  `json:"tier"`
	Target     int     `json:"target"`
	Spread     float64 `json:"spread"`
	// Wallets are every wallet the human ever owned, ascending.
	Wallets []uint64 `json:"wallets"`
	// Gone marks a human with no wallet left.
	Gone bool `json:"gone,omitempty"`
	// Money is the household; nil without a market.
	Money *Money `json:"money,omitempty"`
}

// Money is a human's household money at the checkpoint, in cents.
type Money struct {
	Currency string `json:"currency"`
	// Fiat is what it holds, Held what its open buys hold of that, Fiat0
	// what it started with.
	Fiat  int64 `json:"fiat"`
	Held  int64 `json:"held"`
	Fiat0 int64 `json:"fiat0"`
	// Income is the monthly pay while employed; Ratio the bills over it;
	// Share the part of the rest invested, Steps times a month, timed by
	// Style; Buffer the months of bills kept back.
	Income   int64   `json:"income"`
	Ratio    float64 `json:"ratio"`
	Share    float64 `json:"share"`
	Steps    int     `json:"steps"`
	Style    string  `json:"style"`
	Buffer   float64 `json:"buffer"`
	Employed bool    `json:"employed"`
}

// Market is a wallet's place in the ADA market.
type Market struct {
	// Primary is the currency it thinks in; Extra its other preferred ones.
	Primary string   `json:"primary"`
	Extra   []string `json:"extra,omitempty"`
	// Basis is its average price per ADA in Primary; Units the lovelace
	// the basis covers.
	Basis float64 `json:"basis"`
	Units uint64  `json:"units"`
	// Venue is the exchange it last traded on.
	Venue string `json:"venue,omitempty"`
}

// Keys are an actor's public addresses and identifiers, bech32 or hex.
type Keys struct {
	// PaymentAddr is the wallet's payment address.
	PaymentAddr string `json:"payment_addr,omitempty"`
	// StakeAddr is the wallet's reward address.
	StakeAddr string `json:"stake_addr,omitempty"`
	// PaymentKeyHash is the payment key hash, hex.
	PaymentKeyHash string `json:"payment_key_hash,omitempty"`
	// StakeKeyHash is the stake key hash, hex.
	StakeKeyHash string `json:"stake_key_hash,omitempty"`
	// DRepID is a DRep actor's DRep ID.
	DRepID string `json:"drep_id,omitempty"`
	// PoolID is a pool actor's pool ID.
	PoolID string `json:"pool_id,omitempty"`
}

// Traits are an actor's traits and its state at the checkpoint.
type Traits struct {
	// Competence is the actor's competence trait.
	Competence float64 `json:"competence"`
	// Care is the actor's care trait.
	Care float64 `json:"care"`
	// Patience is the actor's patience trait.
	Patience float64 `json:"patience"`
	// LearningRate is the actor's learning-rate trait.
	LearningRate float64 `json:"learning_rate"`
	// Frustration is the actor's frustration at the checkpoint.
	Frustration float64 `json:"frustration"`
	// Hazard is the per-second dormancy onset rate at the checkpoint.
	Hazard float64 `json:"hazard"`
	// LossAversion is the actor's loss-aversion trait.
	LossAversion float64 `json:"loss_aversion"`
}

// Holdings are the planner ledger's view of an actor's funds.
type Holdings struct {
	// Settled is the actor's lovelace at the settled point.
	Settled uint64 `json:"settled"`
	// UTxOs is how many outputs Settled is in.
	UTxOs int `json:"utxos"`
	// Reserved is the part of Settled unfinished payments hold.
	Reserved uint64 `json:"reserved"`
	// InFlight counts the unfinished payments.
	InFlight int `json:"in_flight"`
	// Unconfirmed is the lovelace those payments pay.
	Unconfirmed uint64 `json:"unconfirmed"`
	// Tokens are the native assets at the chain tip, which may include
	// blocks not yet settled.
	Tokens []Token `json:"tokens,omitempty"`
}

// Delegation is an actor's stake registration and delegations.
type Delegation struct {
	// StakeRegistered reports the stake key registered.
	StakeRegistered bool `json:"stake_registered"`
	// DelegatedPool is the pool the stake is delegated to, bech32.
	DelegatedPool string `json:"delegated_pool,omitempty"`
	// DelegatedActor is the actor operating DelegatedPool, if any.
	DelegatedActor uint64 `json:"delegated_actor,omitempty"`
	// DelegatedDRep is the DRep the stake's votes are delegated to.
	DelegatedDRep string `json:"delegated_drep,omitempty"`
}

// Adversary is an adversary's accounting and profile.
type Adversary struct {
	// CostSpent is the lovelace spent across epochs.
	CostSpent uint64 `json:"cost_spent,omitempty"`
	// CostCeiling is the per-epoch spending ceiling.
	CostCeiling uint64 `json:"cost_ceiling,omitempty"`
	// Metric is the profile's success metric.
	Metric string `json:"success_metric,omitempty"`
	// Budget is the profile's budget.
	Budget uint64 `json:"budget,omitempty"`
	// Attacks are the profile's attacks.
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
