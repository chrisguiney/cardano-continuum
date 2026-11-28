package engine

import (
	"bytes"

	"github.com/chrisguiney/cardano-continuum/internal/adversary"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/floor"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/intent"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/pace"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/pay"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/steer"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/window"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/wire"

	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

// Body, identifier and chain-sync types of the engine's log live in
// internal/engine/internal/wire; these aliases keep their engine names.
type (
	Answer          = wire.Answer
	Attack          = wire.Attack
	BlockContent    = wire.BlockContent
	ChainEvent      = wire.ChainEvent
	Confirmed       = wire.Confirmed
	Control         = wire.Control
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
	Protocol        = wire.Protocol
	Treasury        = wire.Treasury
	ProposalDigest  = wire.ProposalDigest
	Fork            = wire.Fork
	Quote           = wire.Quote
	Mid             = wire.Mid
	Traded          = wire.Traded
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
	KindProtocol     = wire.KindProtocol
	KindTreasury     = wire.KindTreasury
	KindQuote        = wire.KindQuote
	KindTraded       = wire.KindTraded
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
const snapshotVersion = 11

// logFormat is the version of the engine's input and decision bodies. It is
// part of the log header's genesis fingerprint, so a log written in another
// format is refused at the header rather than failing on its first body.
const logFormat = 9

// snapshot is the engine state in canonical form, every slice sorted so
// equal states encode to equal bytes. It is one section per component, in
// the order Restore loads them; each section is the component's own Save.
type snapshot struct {
	// Version is snapshotVersion.
	Version uint32 `json:"version"`
	// Now is the clock: the last Tick's time.
	Now sched.Time `json:"now"`
	// Market is the pace's market.
	Market pace.Market `json:"market"`
	// Contracts is the deployed contracts and the next deployment.
	Contracts intent.State `json:"contracts"`
	// Steer is the operator controls and upgrade waves.
	Steer steer.Saved `json:"steer"`
	// Floor is the pool plan and pod state.
	Floor floor.Saved `json:"floor"`
	// Cohort is every actor and birth stream.
	Cohort cohortRec `json:"cohort"`
	// RefillAt is the faucet's next round; zero without a faucet.
	RefillAt sched.Time `json:"refill_at,omitzero"`
	// Attack is the adversaries' accounts.
	Attack attackRec `json:"attack"`
	// Pay is the planner ledger, its counters and the unfinished intents.
	Pay pay.Saved `json:"pay"`
	// Lost is the lovelace settled payments sent to wrong addresses.
	Lost uint64 `json:"lost,omitzero"`
	// Window is the confirmed point, the volatile blocks and the view.
	Window window.Image `json:"window"`
}

// cohortRec is the cohort's section of the snapshot.
type cohortRec struct {
	// Actors are every actor, ascending.
	Actors []wire.ActorRec `json:"actors"`
	// Births are the birth streams in the order they were added.
	Births []wire.BirthRec `json:"births"`
	// Quote is the market's next Quote record; zero without a market.
	Quote sched.Time `json:"quote_at,omitzero"`
}

// attackRec is the adversaries' section of the snapshot.
type attackRec struct {
	// Charges are the adversaries' spends by epoch.
	Charges []adversary.Charge `json:"charges,omitempty"`
	// Contested are the shared outputs they tried to spend, ascending.
	Contested []Ref `json:"contested,omitempty"`
}

func samePoint(a, b eventlog.ChainPoint) bool {
	return a.Slot == b.Slot && bytes.Equal(a.Hash, b.Hash)
}
