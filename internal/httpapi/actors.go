package httpapi

import (
	"github.com/chrisguiney/cardano-continuum/internal/store"
)

// maxRecent bounds the decisions kept per actor for the detail page.
const maxRecent = 10

// WalletIDs are an actor's public addresses and key hashes. They are
// derived from public keys; no private material ever reaches the API.
type WalletIDs struct {
	PaymentAddr    string `json:"payment_addr"`
	StakeAddr      string `json:"stake_addr"`
	PaymentKeyHash string `json:"payment_key_hash"`
	StakeKeyHash   string `json:"stake_key_hash"`
	DRepID         string `json:"drep_id,omitempty"`
	PoolID         string `json:"pool_id,omitempty"`
}

// Holdings are what an actor owns. Settled counts outputs at the settled
// point; Reserved of it is held by unfinished payments, and Unconfirmed is
// what those payments pay. Tokens are at the chain tip. All are as of the
// engine's last checkpoint.
type Holdings struct {
	SettledLovelace     uint64        `json:"settled_lovelace"`
	ReservedLovelace    uint64        `json:"reserved_lovelace"`
	UnconfirmedLovelace uint64        `json:"unconfirmed_lovelace"`
	UTxOs               int           `json:"utxos"`
	InFlight            int           `json:"in_flight"`
	Tokens              []store.Token `json:"tokens"`
}

// Delegation is an actor's stake registration and delegations.
type Delegation struct {
	StakeRegistered bool   `json:"stake_registered"`
	Pool            string `json:"pool,omitempty"`
	PoolActor       uint64 `json:"pool_actor,omitempty"`
	DRep            string `json:"drep,omitempty"`
}

// Lifecycle is an actor's state and when it last changed. Frustration and
// Hazard (the per-second dormancy onset rate) are as of the checkpoint.
type Lifecycle struct {
	State       string  `json:"state"`
	BornMs      int64   `json:"born_ms"`
	ChangedMs   int64   `json:"changed_ms"`
	Frustration float64 `json:"frustration"`
	Hazard      float64 `json:"hazard"`
}

// Traits are an actor's sampled personal parameters.
type Traits struct {
	Competence   float64 `json:"competence"`
	Care         float64 `json:"care"`
	Patience     float64 `json:"patience"`
	LearningRate float64 `json:"learning_rate"`
}

// Activity counts an actor's intents and outcomes.
type Activity struct {
	Intents      int        `json:"intents"`
	Submitted    int        `json:"submitted"`
	Confirmed    int        `json:"confirmed"`
	Refused      int        `json:"refused"`
	Released     int        `json:"released"`
	GaveUp       int        `json:"gave_up"`
	Retries      int        `json:"retries"`
	LastMs       int64      `json:"last_ms"`
	LostLovelace uint64     `json:"lost_lovelace"`
	Recent       []Decision `json:"recent"`
}

// ActorFinding is an oracle finding that involves one of the actor's
// transactions.
type ActorFinding struct {
	ID       string `json:"id"`
	Seq      uint64 `json:"seq"`
	Kind     string `json:"kind"`
	Severity string `json:"severity"`
	Summary  string `json:"summary"`
}

// AdversaryCost is an adversary's spend against its per-epoch ceiling.
type AdversaryCost struct {
	Spent   uint64 `json:"spent"`
	Ceiling uint64 `json:"ceiling"`
	Metric  string `json:"metric"`
}

// ActorDetail is one actor in full. HasIdentity is false when no store row
// supplies the wallet, holdings, delegation and traits; the rest comes from
// the log.
type ActorDetail struct {
	ID          uint64          `json:"id"`
	Name        string          `json:"name"`
	Profile     string          `json:"profile"`
	Kind        string          `json:"kind"`
	Agenda      string          `json:"agenda"`
	HasIdentity bool            `json:"has_identity"`
	Wallet      WalletIDs       `json:"wallet"`
	Holdings    Holdings        `json:"holdings"`
	Delegation  Delegation      `json:"delegation"`
	Lifecycle   Lifecycle       `json:"lifecycle"`
	Traits      Traits          `json:"traits"`
	Activity    Activity        `json:"activity"`
	Findings    []ActorFinding  `json:"findings"`
	Adversary   *AdversaryCost  `json:"adversary,omitempty"`
	Timeline    []TimelineEntry `json:"timeline"`
}

// shortAddr keeps the prefix and the tail of an address, enough to tell
// two apart in a table.
func shortAddr(a string) string {
	if len(a) <= 20 {
		return a
	}
	return a[:12] + "…" + a[len(a)-6:]
}
