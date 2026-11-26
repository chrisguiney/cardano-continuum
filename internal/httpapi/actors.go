package httpapi

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/chrisguiney/cardano-continuum/internal/agenda"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
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

// withIdentity fills the row's identity columns.
func (r *ActorRow) withIdentity(in store.Identity) {
	r.Kind, r.Address, r.Balance, r.Pending = in.Kind, shortAddr(in.PaymentAddr), in.Settled, in.Unconfirmed
	r.Tokens, r.Delegated, r.Pool, r.DRep = len(in.Tokens), in.DelegatedPool != "", shortAddr(in.DelegatedPool), in.DelegatedDRep
}

func (p *Projection) recordDecision(id uint64, r eventlog.Record, d agenda.Decision) {
	a := p.actor(id)
	a.recent = append(a.recent, Decision{ID: r.Seq, At: int64(r.At), Kind: r.Kind, Actor: id, Intent: string(d.Intent.Kind), Scores: d.Scores})
	if len(a.recent) > maxRecent {
		a.recent = slices.Delete(a.recent, 0, len(a.recent)-maxRecent)
	}
}

// actorLog is what the log knows about one actor.
type actorLog struct {
	info     actorInfo
	timeline []TimelineEntry
}

// ActorLog returns the log's view of one actor, or ErrNotFound.
func (p *Projection) ActorLog(id uint64) (actorLog, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	a := p.actors[id]
	if a == nil {
		return actorLog{}, fmt.Errorf("%w: actor %d", ErrNotFound, id)
	}
	tl := a.tl[max(len(a.tl)-maxTimeline, 0):]
	c := *a
	c.recent, c.tl = slices.Clone(a.recent), nil
	return actorLog{info: c, timeline: slices.Clone(tl)}, nil
}

// TxOwner is the actor that submitted the transaction, if the log shows it.
func (p *Projection) TxOwner(tx []byte) (uint64, bool) {
	if len(tx) != 32 {
		return 0, false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	id, ok := p.txOwner[[32]byte(tx)]
	return id, ok
}

// ActorDetail implements Source.
func (s *LogSource) ActorDetail(ctx context.Context, id uint64) (ActorDetail, error) {
	l, err := s.cfg.Projection.ActorLog(id)
	if err != nil {
		return ActorDetail{}, err
	}
	n, err := s.names(ctx)
	if err != nil {
		return ActorDetail{}, err
	}
	row := n[id]
	d := ActorDetail{ID: id, Name: row.Name, Profile: l.info.profile, Timeline: l.timeline, Findings: s.actorFindings(id)}
	if d.Profile == "" {
		d.Profile = row.Profile
	}
	in, err := store.DecodeIdentity(row.Identity)
	if err != nil {
		return ActorDetail{}, fmt.Errorf("actor %d: %w", id, err)
	}
	d.HasIdentity = row.Identity != ""
	d.Kind, d.Agenda = in.Kind, in.Agenda
	d.Wallet = WalletIDs{in.PaymentAddr, in.StakeAddr, in.PaymentKeyHash, in.StakeKeyHash, in.DRepID, in.PoolID}
	d.Holdings = Holdings{in.Settled, in.Reserved, in.Unconfirmed, in.UTxOs, in.InFlight, in.Tokens}
	if d.Holdings.Tokens == nil {
		d.Holdings.Tokens = []store.Token{}
	}
	d.Delegation = Delegation{in.StakeRegistered, in.DelegatedPool, in.DelegatedActor, in.DelegatedDRep}
	d.Traits = Traits{in.Competence, in.Care, in.Patience, in.LearningRate}
	d.Lifecycle = Lifecycle{State: l.info.state, BornMs: l.info.born, ChangedMs: l.info.changed, Frustration: in.Frustration, Hazard: in.Hazard}
	i := l.info
	d.Activity = Activity{Intents: i.intents, Submitted: i.txs, Confirmed: i.confirmed, Refused: i.refused, Released: i.released,
		GaveUp: i.gaveUp, Retries: i.retries, LastMs: i.last, LostLovelace: i.lost, Recent: i.recent}
	if d.Activity.Recent == nil {
		d.Activity.Recent = []Decision{}
	}
	if in.CostCeiling > 0 {
		d.Adversary = &AdversaryCost{in.CostSpent, in.CostCeiling, in.Metric}
	}
	return d, nil
}

// actorFindings lists the findings whose transaction the actor submitted.
func (s *LogSource) actorFindings(id uint64) []ActorFinding {
	fs := s.findingsSet()
	_, n := fs.Page(0, 0)
	all, _ := fs.Page(0, n)
	out := []ActorFinding{}
	for _, f := range all {
		if owner, ok := s.cfg.Projection.TxOwner(f.TxHash); ok && owner == id {
			out = append(out, ActorFinding{ID: f.ID, Seq: f.Seq, Kind: string(f.Kind), Severity: f.Severity.String(), Summary: strings.TrimSpace(f.Summary)})
		}
	}
	return out
}

func (s *LogSource) findingsSet() Findings {
	if s.cfg.Findings != nil {
		return s.cfg.Findings
	}
	return s.cfg.Projection.findings
}
