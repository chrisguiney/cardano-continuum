package wire

import (
	"github.com/blinklabs-io/gouroboros/ledger/common"

	"github.com/chrisguiney/cardano-continuum/internal/chain"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

// The records below are the components' sections of the engine snapshot.
// Their encoding is the snapshot's: changing, renaming or reordering a
// field changes the checkpoint bytes and needs a snapshot version bump.

// ActorRec is one actor as the snapshot records it: its census entry and
// the in-memory record the cohort keeps beside it.
type ActorRec struct {
	ID          uint64     `json:"id"`
	Profile     string     `json:"profile"`
	State       string     `json:"state"`
	Born        sched.Time `json:"born"`
	Changed     sched.Time `json:"changed"`
	Frustration float64    `json:"frustration"`
	HazardAt    sched.Time `json:"hazard_at"`
	HazardTo    string     `json:"hazard_to"`
	ActAt       sched.Time `json:"act_at"`
	Draws       uint64     `json:"draws"`
	Competence  float64    `json:"competence"`
	// Booting marks a population actor's wake drawn at the boot rate.
	Booting bool `json:"booting,omitzero"`
	// Owed is the follow-ups the actor owes.
	Owed uint32 `json:"owed,omitzero"`
	// Paid are the actor's known payees, most recent last.
	Paid []uint64 `json:"paid,omitempty"`
	// FollowUp marks the pending wake as excited.
	FollowUp bool `json:"follow_up,omitzero"`
	// Waits is the blocks the pending follow-up has waited.
	Waits uint8 `json:"waits,omitzero"`
}

// BirthRec is one birth stream: its profile, next birth and draw count.
type BirthRec struct {
	Profile string     `json:"profile"`
	At      sched.Time `json:"at"`
	N       uint64     `json:"n"`
}

// ViewRec is chain.State in the snapshot. Outputs travel as CBOR, since a
// ledger output is an interface.
type ViewRec struct {
	At        eventlog.ChainPoint   `json:"at"`
	Height    uint64                `json:"height"`
	Utxos     []UtxoRec             `json:"utxos"`
	Stake     []chain.StakeState    `json:"stake"`
	Pools     []chain.PoolState     `json:"pools"`
	DReps     []common.Blake2b224   `json:"d_reps"`
	Label     uint64                `json:"label"`
	Frozen    bool                  `json:"frozen"`
	Mark      chain.Snapshot        `json:"mark"`
	Set       chain.Snapshot        `json:"set"`
	Proposals []chain.ProposalState `json:"proposals"`
	Votes     []chain.VoteState     `json:"votes"`
}

// UtxoRec is one output of the view, its CBOR as the block carried it.
type UtxoRec struct {
	Ref Ref    `json:"ref"`
	Out []byte `json:"out"`
}

// WeightRec is one operator weight override: an intent kind and its weight.
type WeightRec struct {
	Kind   string  `json:"kind"`
	Weight float64 `json:"weight"`
}

// ImageRec is the node image an upgrade wave moved a pool actor to.
type ImageRec struct {
	Pool uint64 `json:"pool"`
	Ref  string `json:"ref"`
}
