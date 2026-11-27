package wire

import (
	"github.com/blinklabs-io/gouroboros/ledger/common"

	"github.com/chrisguiney/cardano-continuum/internal/chain"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

// The records below are the components' sections of the engine snapshot.
// Their encoding is the snapshot's: changing a field or a key changes the
// checkpoint bytes and needs a snapshot version bump.

// ActorRec is one actor as the snapshot records it: its census entry and
// the in-memory record the cohort keeps beside it.
type ActorRec struct {
	ID          uint64     `cbor:"1,keyasint"`
	Profile     string     `cbor:"2,keyasint"`
	State       string     `cbor:"3,keyasint"`
	Born        sched.Time `cbor:"4,keyasint"`
	Changed     sched.Time `cbor:"5,keyasint"`
	Frustration float64    `cbor:"6,keyasint"`
	HazardAt    sched.Time `cbor:"7,keyasint"`
	HazardTo    string     `cbor:"8,keyasint"`
	ActAt       sched.Time `cbor:"9,keyasint"`
	Draws       uint64     `cbor:"10,keyasint"`
	Competence  float64    `cbor:"11,keyasint"`
	// Booting marks a population actor's wake drawn at the boot rate.
	Booting bool `cbor:"12,keyasint,omitempty"`
	// Owed is the follow-ups the actor owes.
	Owed uint32 `cbor:"13,keyasint,omitempty"`
	// Paid are the actor's known payees, most recent last.
	Paid []uint64 `cbor:"15,keyasint,omitempty"`
	// FollowUp marks the pending wake as excited.
	FollowUp bool `cbor:"16,keyasint,omitempty"`
	// Waits is the blocks the pending follow-up has waited.
	Waits uint8 `cbor:"17,keyasint,omitempty"`
}

// BirthRec is one birth stream: its profile, next birth and draw count.
type BirthRec struct {
	Profile string     `cbor:"1,keyasint"`
	At      sched.Time `cbor:"2,keyasint"`
	N       uint64     `cbor:"3,keyasint"`
}

// ViewRec is chain.State in the snapshot. Outputs travel as CBOR, since a
// ledger output is an interface.
type ViewRec struct {
	At        eventlog.ChainPoint   `cbor:"1,keyasint"`
	Height    uint64                `cbor:"2,keyasint"`
	Utxos     []UtxoRec             `cbor:"3,keyasint"`
	Stake     []chain.StakeState    `cbor:"4,keyasint"`
	Pools     []chain.PoolState     `cbor:"5,keyasint"`
	DReps     []common.Blake2b224   `cbor:"6,keyasint"`
	Label     uint64                `cbor:"7,keyasint"`
	Frozen    bool                  `cbor:"8,keyasint"`
	Mark      chain.Snapshot        `cbor:"9,keyasint"`
	Set       chain.Snapshot        `cbor:"10,keyasint"`
	Proposals []chain.ProposalState `cbor:"11,keyasint"`
	Votes     []chain.VoteState     `cbor:"12,keyasint"`
}

// UtxoRec is one output of the view, its CBOR as the block carried it.
type UtxoRec struct {
	Ref Ref    `cbor:"1,keyasint"`
	Out []byte `cbor:"2,keyasint"`
}

// WeightRec is one operator weight override: an intent kind and its weight.
type WeightRec struct {
	Kind   string  `cbor:"1,keyasint"`
	Weight float64 `cbor:"2,keyasint"`
}

// ImageRec is the node image an upgrade wave moved a pool actor to.
type ImageRec struct {
	Pool uint64 `cbor:"1,keyasint"`
	Ref  string `cbor:"2,keyasint"`
}
