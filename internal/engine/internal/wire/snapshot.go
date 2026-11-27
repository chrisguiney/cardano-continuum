package wire

import "github.com/chrisguiney/cardano-continuum/internal/sched"

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
