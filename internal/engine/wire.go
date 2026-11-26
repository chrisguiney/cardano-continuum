package engine

import (
	"bytes"

	"github.com/chrisguiney/cardano-continuum/internal/adversary"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/wire"

	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/mistake"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
	"github.com/chrisguiney/cardano-continuum/internal/submit"
)

// Body, identifier and chain-sync types of the engine's log live in
// internal/engine/internal/wire; these aliases keep their engine names.
type (
	Answer          = wire.Answer
	Attack          = wire.Attack
	BlockContent    = wire.BlockContent
	ChainEvent      = wire.ChainEvent
	Confirmed       = wire.Confirmed
	Controlled      = wire.Controlled
	Deployed        = wire.Deployed
	Follower        = wire.Follower
	GaveUp          = wire.GaveUp
	Guarded         = wire.Guarded
	ImageChange     = wire.ImageChange
	Incident        = wire.Incident
	MonitorSnapshot = wire.MonitorSnapshot
	PoolStatus      = wire.PoolStatus
	PoolTier        = wire.PoolTier
	ProposalDigest  = wire.ProposalDigest
	Ref             = wire.Ref
	Refill          = wire.Refill
	Refused         = wire.Refused
	Released        = wire.Released
	Repair          = wire.Repair
	Rival           = wire.Rival
	Skipped         = wire.Skipped
	Spawned         = wire.Spawned
	SubmitResult    = wire.SubmitResult
	Submitted       = wire.Submitted
	Transition      = wire.Transition
	TxDigest        = wire.TxDigest
	TxID            = wire.TxID
	TxOut           = wire.TxOut
	Verdict         = wire.Verdict
	VoteDigest      = wire.VoteDigest
)

// Decision kinds and verdicts, aliased from wire.
const (
	KindAttack       = wire.KindAttack
	KindConfirmed    = wire.KindConfirmed
	KindControlled   = wire.KindControlled
	KindDeployed     = wire.KindDeployed
	KindGaveUp       = wire.KindGaveUp
	KindGuarded      = wire.KindGuarded
	KindImageChange  = wire.KindImageChange
	KindIncident     = wire.KindIncident
	KindIntentChosen = wire.KindIntentChosen
	KindPoolTier     = wire.KindPoolTier
	KindRefill       = wire.KindRefill
	KindRefused      = wire.KindRefused
	KindReleased     = wire.KindReleased
	KindRepair       = wire.KindRepair
	KindSkipped      = wire.KindSkipped
	KindSpawned      = wire.KindSpawned
	KindSubmitted    = wire.KindSubmitted
	KindTransition   = wire.KindTransition
	VerdictAccepted  = wire.VerdictAccepted
	VerdictRejected  = wire.VerdictRejected
	VerdictUnreached = wire.VerdictUnreached
	VerdictDropped   = wire.VerdictDropped
)

// DecisionKinds returns every decision kind the engine emits.
func DecisionKinds() []string { return wire.DecisionKinds() }

// Digest reads a Conway block into BlockContent and encodes it; see wire.Digest.
func Digest(blockCbor []byte) ([]byte, error) { return wire.Digest(blockCbor) }

// snapshotVersion is bumped whenever snapshot's shape changes.
const snapshotVersion = 7

// logFormat is the version of the engine's input and decision bodies. It is
// part of the log header's genesis fingerprint, so a log written in another
// format is refused at the header rather than failing on its first body.
const logFormat = 5

// snapshot is the engine state in canonical form: every slice sorted, so
// equal states encode to equal bytes.
type snapshot struct {
	Version    uint32              `cbor:"1,keyasint"`
	Now        sched.Time          `cbor:"2,keyasint"`
	NextIntent uint64              `cbor:"3,keyasint"`
	Actors     []actorRec          `cbor:"4,keyasint"`
	Births     []birthRec          `cbor:"5,keyasint"`
	Conf       eventlog.ChainPoint `cbor:"6,keyasint"`
	Window     []blockRec          `cbor:"7,keyasint"`
	Pending    []pendingRec        `cbor:"8,keyasint"`
	Fees       uint64              `cbor:"9,keyasint"`
	Foreign    uint64              `cbor:"10,keyasint"`
	Ledger     ledgerBlob          `cbor:"11,keyasint"`
	// RefillAt is the faucet's next round; zero without a faucet.
	RefillAt sched.Time `cbor:"12,keyasint,omitempty"`
	Deposits uint64     `cbor:"14,keyasint,omitempty"`
	Market   market     `cbor:"15,keyasint"`
	// Dark, Down and Repairs are the pool plan and pod state, ascending.
	Dark    []uint64 `cbor:"16,keyasint,omitempty"`
	Down    []uint64 `cbor:"17,keyasint,omitempty"`
	Repairs []uint64 `cbor:"18,keyasint,omitempty"`
	Stalled bool     `cbor:"19,keyasint,omitempty"`
	// Controls and upgrade waves; Images is (pool actor, image) pairs,
	// ascending.
	Paused    bool        `cbor:"20,keyasint,omitempty"`
	RateK     uint32      `cbor:"21,keyasint,omitempty"`
	Weights   []weightRec `cbor:"22,keyasint,omitempty"`
	Waves     uint64      `cbor:"23,keyasint,omitempty"`
	Adoptions []adoption  `cbor:"24,keyasint,omitempty"`
	Images    []imageRec  `cbor:"25,keyasint,omitempty"`
	// View is the chain view at the confirmed point; the window is replayed
	// onto it on restore.
	View viewRec `cbor:"13,keyasint"`
	// Lost is the lovelace settled payments sent to wrong addresses.
	Lost uint64 `cbor:"26,keyasint,omitempty"`
	// Charges are the adversaries' spends by epoch; Contested the shared
	// outputs they tried to spend, ascending.
	Charges   []adversary.Charge `cbor:"27,keyasint,omitempty"`
	Contested []Ref              `cbor:"28,keyasint,omitempty"`
	// Contracts is how many contracts are deployed; DeployAt and DeployN
	// are the next deployment and its draw.
	Contracts uint64     `cbor:"29,keyasint,omitempty"`
	DeployAt  sched.Time `cbor:"30,keyasint,omitempty"`
	DeployN   uint64     `cbor:"31,keyasint,omitempty"`
}

// ledgerBlob is the planner ledger's store.Blob.
type ledgerBlob struct {
	Version uint32 `cbor:"1,keyasint"`
	Data    []byte `cbor:"2,keyasint"`
}

type actorRec struct {
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
	// Owed is the follow-ups the actor owes (engine.go, nextAct).
	Owed uint32 `cbor:"13,keyasint,omitempty"`
	// Paid are the actor's known payees, most recent last.
	Paid []uint64 `cbor:"15,keyasint,omitempty"`
	// FollowUp marks the pending wake as excited (engine.go, act).
	FollowUp bool `cbor:"16,keyasint,omitempty"`
	// Waits is the blocks the pending follow-up has waited.
	Waits uint8 `cbor:"17,keyasint,omitempty"`
}

type birthRec struct {
	Profile string     `cbor:"1,keyasint"`
	At      sched.Time `cbor:"2,keyasint"`
	N       uint64     `cbor:"3,keyasint"`
}

type blockRec struct {
	Point  eventlog.ChainPoint `cbor:"1,keyasint"`
	Txs    []TxDigest          `cbor:"2,keyasint"`
	Number uint64              `cbor:"3,keyasint,omitempty"`
}

// pendingRec is a payment the ledger holds unfinished. Its inputs and
// reservation are the ledger's.
type pendingRec struct {
	ID    uint64 `cbor:"1,keyasint"`
	Actor uint64 `cbor:"2,keyasint"`
	// Tx and TTL are the current attempt's.
	Tx  TxID   `cbor:"3,keyasint"`
	TTL uint64 `cbor:"4,keyasint"`
	// Cbor is held while the current attempt may still be sent, so a
	// restart or a resend can submit the same bytes.
	Cbor     []byte `cbor:"5,keyasint"`
	Awaiting bool   `cbor:"6,keyasint"`
	// M is the retry machine; To and Amount are what a rebuild pays.
	M      submit.Machine `cbor:"7,keyasint"`
	To     uint64         `cbor:"8,keyasint"`
	Amount uint64         `cbor:"9,keyasint"`
	// Txs are every unfinished attempt of the payment, oldest first. Only
	// the ledger removes one: settled, dead, or refused by every node.
	Txs []TxID `cbor:"10,keyasint"`
	// NotBefore is when the current attempt may next be sent: the backoff.
	NotBefore sched.Time `cbor:"11,keyasint,omitempty"`
	// Refill marks the faucet's refill, which M does not drive.
	Refill bool `cbor:"12,keyasint,omitempty"`
	// Kind is the intent's builder (intents.go); empty is a payment. To is
	// the payee, pool or DRep it names, Amount what a payment pays, a lock
	// locks, a mint or burn moves, or an unlock frees.
	Kind string `cbor:"13,keyasint,omitempty"`
	// Action is the governance action a vote is on, Choice the vote.
	Action *Ref  `cbor:"14,keyasint,omitempty"`
	Choice uint8 `cbor:"15,keyasint,omitempty"`
	// Locked is the script output an unlock spends.
	Locked *Ref `cbor:"16,keyasint,omitempty"`
	// Epoch is the epoch a retirement takes effect.
	Epoch uint64 `cbor:"17,keyasint,omitempty"`
	// Register makes a delegation also register the stake key; Update
	// makes a pool registration a re-registration.
	Register bool `cbor:"18,keyasint,omitempty"`
	Update   bool `cbor:"19,keyasint,omitempty"`
	// Injected is the mistake sampled for the intent; Carried those the
	// current attempt carries (Injected less every fix so far), and Lost
	// what the current attempt pays to a wrong address.
	Injected mistake.KindSet `cbor:"20,keyasint,omitempty"`
	Carried  mistake.KindSet `cbor:"21,keyasint,omitempty"`
	Lost     uint64          `cbor:"22,keyasint,omitempty"`
	// Attack names the attack of an adversary's step (attack.go), which
	// no retry machine drives; Want is the verdict its first send calls
	// for, Rivals a race's further transactions still to send, and Jump
	// sends it ahead of every other submission (a front-run).
	Attack string   `cbor:"23,keyasint,omitempty"`
	Want   *wantRec `cbor:"24,keyasint,omitempty"`
	Rivals []Rival  `cbor:"25,keyasint,omitempty"`
	Jump   bool     `cbor:"26,keyasint,omitempty"`
	// GovType is a proposal's governance action type; Contract the
	// contract a lock or unlock calls.
	GovType  string `cbor:"27,keyasint,omitempty"`
	Contract uint64 `cbor:"28,keyasint,omitempty"`
	// Power is a vote's voting power when it was chosen. Probe marks a
	// vote the ledger must refuse (an SPO vote on an action pools may not
	// vote on): its expected verdict is DisallowedVoters.
	Power uint64 `cbor:"29,keyasint,omitempty"`
	Probe bool   `cbor:"30,keyasint,omitempty"`
}

func samePoint(a, b eventlog.ChainPoint) bool {
	return a.Slot == b.Slot && bytes.Equal(a.Hash, b.Hash)
}

type weightRec struct {
	Kind   string  `cbor:"1,keyasint"`
	Weight float64 `cbor:"2,keyasint"`
}

type imageRec struct {
	Pool uint64 `cbor:"1,keyasint"`
	Ref  string `cbor:"2,keyasint"`
}
