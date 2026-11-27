package engine

import (
	"bytes"

	"github.com/chrisguiney/cardano-continuum/internal/adversary"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/pace"
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
const snapshotVersion = 8

// logFormat is the version of the engine's input and decision bodies. It is
// part of the log header's genesis fingerprint, so a log written in another
// format is refused at the header rather than failing on its first body.
const logFormat = 6

// snapshot is the engine state in canonical form: every slice sorted, so
// equal states encode to equal bytes.
type snapshot struct {
	Version    uint32              `json:"version"`
	Now        sched.Time          `json:"now"`
	NextIntent uint64              `json:"next_intent"`
	Actors     []wire.ActorRec     `json:"actors"`
	Births     []wire.BirthRec     `json:"births"`
	Conf       eventlog.ChainPoint `json:"conf"`
	Window     []blockRec          `json:"window"`
	Pending    []pendingRec        `json:"pending"`
	Fees       uint64              `json:"fees"`
	Foreign    uint64              `json:"foreign"`
	Ledger     ledgerBlob          `json:"ledger"`
	// RefillAt is the faucet's next round; zero without a faucet.
	RefillAt sched.Time  `json:"refill_at,omitzero"`
	Deposits uint64      `json:"deposits,omitzero"`
	Market   pace.Market `json:"market"`
	// Dark, Down and Repairs are the pool plan and pod state, ascending.
	Dark    []uint64 `json:"dark,omitempty"`
	Down    []uint64 `json:"down,omitempty"`
	Repairs []uint64 `json:"repairs,omitempty"`
	Stalled bool     `json:"stalled,omitzero"`
	// Controls and upgrade waves; Images is (pool actor, image) pairs,
	// ascending.
	Paused    bool             `json:"paused,omitzero"`
	RateK     uint32           `json:"rate_k,omitzero"`
	Weights   []wire.WeightRec `json:"weights,omitempty"`
	Waves     uint64           `json:"waves,omitzero"`
	Adoptions []adoption       `json:"adoptions,omitempty"`
	Images    []wire.ImageRec  `json:"images,omitempty"`
	// View is the chain view at the confirmed point; the window is replayed
	// onto it on restore.
	View wire.ViewRec `json:"view"`
	// Lost is the lovelace settled payments sent to wrong addresses.
	Lost uint64 `json:"lost,omitzero"`
	// Charges are the adversaries' spends by epoch; Contested the shared
	// outputs they tried to spend, ascending.
	Charges   []adversary.Charge `json:"charges,omitempty"`
	Contested []Ref              `json:"contested,omitempty"`
	// Contracts is how many contracts are deployed; DeployAt and DeployN
	// are the next deployment and its draw.
	Contracts uint64     `json:"contracts,omitzero"`
	DeployAt  sched.Time `json:"deploy_at,omitzero"`
	DeployN   uint64     `json:"deploy_n,omitzero"`
}

// ledgerBlob is the planner ledger's store.Blob.
type ledgerBlob struct {
	Version uint32 `json:"version"`
	Data    []byte `json:"data"`
}

type blockRec = wire.Block

// pendingRec is wire.Pending as the snapshot records it; the field
// comments are on wire.Pending's parts.
type pendingRec struct {
	ID    uint64 `json:"id"`
	Actor uint64 `json:"actor"`
	// Tx and TTL are the current attempt's.
	Tx  TxID   `json:"tx"`
	TTL uint64 `json:"ttl"`
	// Cbor is held while the current attempt may still be sent, so a
	// restart or a resend can submit the same bytes.
	Cbor     []byte `json:"cbor"`
	Awaiting bool   `json:"awaiting"`
	// M is the retry machine; To and Amount are what a rebuild pays.
	M      submit.Machine `json:"m"`
	To     uint64         `json:"to"`
	Amount uint64         `json:"amount"`
	// Txs are every unfinished attempt of the payment, oldest first. Only
	// the ledger removes one: settled, dead, or refused by every node.
	Txs []TxID `json:"txs"`
	// NotBefore is when the current attempt may next be sent: the backoff.
	NotBefore sched.Time `json:"not_before,omitzero"`
	// Refill marks the faucet's refill, which M does not drive.
	Refill bool `json:"refill,omitzero"`
	// Kind is the intent's builder (internal/intent); empty is a payment. To is
	// the payee, pool or DRep it names, Amount what a payment pays, a lock
	// locks, a mint or burn moves, or an unlock frees.
	Kind string `json:"kind,omitempty"`
	// Action is the governance action a vote is on, Choice the vote.
	Action *Ref  `json:"action,omitzero"`
	Choice uint8 `json:"choice,omitzero"`
	// Locked is the script output an unlock spends.
	Locked *Ref `json:"locked,omitzero"`
	// Epoch is the epoch a retirement takes effect.
	Epoch uint64 `json:"epoch,omitzero"`
	// Register makes a delegation also register the stake key; Update
	// makes a pool registration a re-registration.
	Register bool `json:"register,omitzero"`
	Update   bool `json:"update,omitzero"`
	// Injected is the mistake sampled for the intent; Carried those the
	// current attempt carries (Injected less every fix so far), and Lost
	// what the current attempt pays to a wrong address.
	Injected mistake.KindSet `json:"injected,omitzero"`
	Carried  mistake.KindSet `json:"carried,omitzero"`
	Lost     uint64          `json:"lost,omitzero"`
	// Attack names the attack of an adversary's step (attack.go), which
	// no retry machine drives; Want is the verdict its first send calls
	// for, Rivals a race's further transactions still to send, and Jump
	// sends it ahead of every other submission (a front-run).
	Attack string   `json:"attack,omitempty"`
	Want   *wantRec `json:"want,omitzero"`
	Rivals []Rival  `json:"rivals,omitempty"`
	Jump   bool     `json:"jump,omitzero"`
	// GovType is a proposal's governance action type; Contract the
	// contract a lock or unlock calls.
	GovType  string `json:"gov_type,omitempty"`
	Contract uint64 `json:"contract,omitzero"`
	// Power is a vote's voting power when it was chosen. Probe marks a
	// vote the ledger must refuse (an SPO vote on an action pools may not
	// vote on): its expected verdict is DisallowedVoters.
	Power uint64 `json:"power,omitzero"`
	Probe bool   `json:"probe,omitzero"`
}

// recOf is p as the snapshot records it.
func recOf(p *wire.Pending) pendingRec {
	r := pendingRec{ID: p.ID, Actor: p.Actor, Refill: p.Refill,
		Tx: p.Tx, TTL: p.TTL, Cbor: p.Cbor, Awaiting: p.Awaiting, M: p.M, Txs: p.Txs, NotBefore: p.NotBefore,
		Kind: p.Kind, To: p.To, Amount: p.Amount, Locked: p.Locked, Contract: p.Contract, Epoch: p.Epoch, Register: p.Register, Update: p.Update,
		Action: p.Action, Choice: p.Choice, GovType: p.GovType, Power: p.Power, Probe: p.Probe,
		Injected: p.Injected, Carried: p.Carried, Lost: p.Lost,
		Attack: p.Attack, Rivals: p.Rivals, Jump: p.Jump}
	if p.Want != nil {
		r.Want = &wantRec{Classes: p.Want.Classes, Accept: p.Want.Accept}
	}
	return r
}

// pendingOf is the intent r records.
func pendingOf(r pendingRec) *wire.Pending {
	p := &wire.Pending{ID: r.ID, Actor: r.Actor, Refill: r.Refill,
		Attempt: wire.Attempt{Tx: r.Tx, TTL: r.TTL, Cbor: r.Cbor, Awaiting: r.Awaiting, M: r.M, Txs: r.Txs, NotBefore: r.NotBefore},
		Args:    wire.Args{Kind: r.Kind, To: r.To, Amount: r.Amount, Locked: r.Locked, Contract: r.Contract, Epoch: r.Epoch, Register: r.Register, Update: r.Update},
		Gov:     wire.Gov{Action: r.Action, Choice: r.Choice, GovType: r.GovType, Power: r.Power, Probe: r.Probe},
		Faults:  wire.Faults{Injected: r.Injected, Carried: r.Carried, Lost: r.Lost},
		Strike:  wire.Strike{Attack: r.Attack, Rivals: r.Rivals, Jump: r.Jump}}
	if r.Want != nil {
		p.Want = &wire.Want{Classes: r.Want.Classes, Accept: r.Want.Accept}
	}
	return p
}

func samePoint(a, b eventlog.ChainPoint) bool {
	return a.Slot == b.Slot && bytes.Equal(a.Hash, b.Hash)
}

// wantRec is the verdict an attack's first send calls for
// (mistake.Expected).
type wantRec struct {
	Classes []string `json:"classes,omitempty"`
	Accept  bool     `json:"accept,omitzero"`
}
