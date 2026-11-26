package view

import (
	"errors"

	"github.com/chrisguiney/cardano-continuum/internal/agenda"
	"github.com/chrisguiney/cardano-continuum/internal/store"
)

// Errors the handlers map to HTTP statuses. Wrap them with the detail.
var (
	// ErrBadRequest marks a client mistake: a malformed or out-of-range
	// parameter or payload (HTTP 400).
	ErrBadRequest = errors.New("bad request")
	// ErrNotFound marks an unknown actor or finding (HTTP 404).
	ErrNotFound = errors.New("not found")
	// ErrTooManyClients is returned by Subscribe at the client cap (HTTP 429).
	ErrTooManyClients = errors.New("too many feed clients")
	// ErrClosed is returned by Subscribe after the hub is closed.
	ErrClosed = errors.New("feed closed")
)

// Paging bounds. They match the store's actor paging.
const (
	DefaultLimit = 100
	MaxLimit     = 500
	MaxOffset    = 1 << 30
)

// Page is a limit and offset into a list.
type Page struct {
	Limit  int
	Offset int
	// Known filters findings by whether they match a known issue: KnownAll
	// (the zero value), KnownHide or KnownOnly.
	Known KnownFilter
	// Kind and Severity, when set, keep only findings of that oracle.Kind
	// and severity name.
	Kind     string
	Severity string
}

// KnownFilter selects findings by their known-issue tag.
type KnownFilter string

// Known-issue filters.
const (
	KnownAll  KnownFilter = ""
	KnownHide KnownFilter = "hide"
	KnownOnly KnownFilter = "only"
)

// Point is a chain point: a slot and a hex block hash.
type Point struct {
	Slot uint64 `json:"slot"`
	Hash string `json:"hash"`
	// Height is the block number; zero when the source does not know it.
	Height uint64 `json:"height,omitempty"`
}

// Snapshot is the Overview view.
type Snapshot struct {
	LastSeq   uint64 `json:"last_seq"`
	SimTimeMs int64  `json:"sim_time_ms"`
	// Tip is the last chain point the engine applied; nil before the first block.
	Tip       *Point `json:"tip"`
	Blocks    uint64 `json:"blocks"`
	Rollbacks uint64 `json:"rollbacks"`
	// Epoch is nil when the slot configuration is unknown.
	Epoch *uint64 `json:"epoch"`
	// TxPerSec and SubmitPerSec are confirmations and submissions per second
	// of simulated time.
	TxPerSec     float64     `json:"tx_per_sec"`
	SubmitPerSec float64     `json:"submit_per_sec"`
	Ada          Ada         `json:"ada"`
	Txs          TxCounts    `json:"txs"`
	Population   Population  `json:"population"`
	Invariants   []Invariant `json:"invariants"`
}

// Ada is the lovelace accounting visible in the log.
type Ada struct {
	// FeesLovelace is the fees of transactions seen in applied blocks.
	FeesLovelace uint64 `json:"fees_lovelace"`
	// SubmittedLovelace sums the payment amount of every attempt;
	// ConfirmedLovelace sums confirmed payments once.
	SubmittedLovelace uint64 `json:"submitted_lovelace"`
	ConfirmedLovelace uint64 `json:"confirmed_lovelace"`
}

// TxCounts are payment outcomes.
type TxCounts struct {
	Submitted uint64 `json:"submitted"`
	Confirmed uint64 `json:"confirmed"`
	Retries   uint64 `json:"retries"`
	Refused   uint64 `json:"refused"`
	Released  uint64 `json:"released"`
	GaveUp    uint64 `json:"gave_up"`
	Skipped   uint64 `json:"skipped"`
}

// Population counts actors.
type Population struct {
	Total     int            `json:"total"`
	ByProfile map[string]int `json:"by_profile"`
	ByState   map[string]int `json:"by_state"`
}

// Invariant is one health light.
type Invariant struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

// ActorFilter selects actors; an empty field matches all.
type ActorFilter struct {
	Profile string
	State   string
	Role    string
	// Kind is the actor kind (profile.Kind).
	Kind string
	// Delegated, when set, keeps only actors that are (true) or are not
	// (false) delegated to a pool.
	Delegated *bool
}

// ActorQuery pages, sorts and filters the actor table.
type ActorQuery struct {
	Sort   string
	Desc   bool
	Offset int
	Limit  int
	Filter ActorFilter
}

// ActorRow is one row of the Actors table. Name and Role come from the
// store's read model and are empty until a checkpoint holds the actor.
type ActorRow struct {
	ID        uint64 `json:"id"`
	Name      string `json:"name"`
	Role      string `json:"role"`
	Profile   string `json:"profile"`
	State     string `json:"state"`
	Intents   int    `json:"intents"`
	Txs       int    `json:"txs"`
	Retries   int    `json:"retries"`
	Confirmed int    `json:"confirmed"`
	Released  int    `json:"released"`
	// The columns below come from the engine's published identity, as of
	// its last checkpoint, and are zero when the store has none.
	Kind      string `json:"kind"`
	Address   string `json:"address"`
	Balance   uint64 `json:"balance"`
	Pending   uint64 `json:"pending"`
	Tokens    int    `json:"tokens"`
	Delegated bool   `json:"delegated"`
	Pool      string `json:"pool"`
	DRep      string `json:"drep"`
}

// ActorPage is one page of actors; Total counts the filtered set.
type ActorPage struct {
	Total  int        `json:"total"`
	Offset int        `json:"offset"`
	Limit  int        `json:"limit"`
	Rows   []ActorRow `json:"rows"`
}

// TimelineEntry is one event in an actor's life.
type TimelineEntry struct {
	Seq    uint64  `json:"seq"`
	At     int64   `json:"at"`
	Kind   string  `json:"kind"`
	Detail string  `json:"detail,omitempty"`
	Intent *uint64 `json:"intent,omitempty"`
}

// Timeline is one actor's profile traits and its intents, transactions,
// retries and outcomes, oldest first. Dropped counts older entries the
// server no longer holds.
type Timeline struct {
	Actor   uint64          `json:"actor"`
	Profile string          `json:"profile"`
	Role    string          `json:"role"`
	State   string          `json:"state"`
	BornMs  int64           `json:"born_ms"`
	Dropped int             `json:"dropped"`
	Entries []TimelineEntry `json:"entries"`
}

// PoolRow is one pool. Stake, BlocksMade and BlocksExpected are nil until a
// source knows them.
type PoolRow struct {
	ID             string   `json:"id"`
	PoolID         string   `json:"pool_id"`
	Impl           string   `json:"impl"`
	Tier           string   `json:"tier"`
	Phase          string   `json:"phase"`
	Dark           bool     `json:"dark"`
	Tip            Point    `json:"tip"`
	Image          string   `json:"image,omitempty"`
	Fork           bool     `json:"fork"`
	Stake          *uint64  `json:"stake"`
	BlocksMade     *uint64  `json:"blocks_made"`
	BlocksExpected *float64 `json:"blocks_expected"`
}

// ImplTip is the furthest tip among one implementation's pools.
type ImplTip struct {
	Impl   string `json:"impl"`
	Slot   uint64 `json:"slot"`
	Hash   string `json:"hash"`
	Height uint64 `json:"height,omitempty"`
	Pools  int    `json:"pools"`
}

// ForkGroup is the pools that agree on one block hash.
type ForkGroup struct {
	Hash  string   `json:"hash"`
	Pools []string `json:"pools"`
}

// Fork reasons.
const (
	// ReasonDiverged: pools hold different blocks at the same height.
	ReasonDiverged = "diverged"
	// ReasonBehind: a pool's tip is more than k blocks below the furthest.
	ReasonBehind = "behind"
)

// Fork marks pools whose chains disagree. For ReasonDiverged, Height is
// the common height compared and Depth the number of blocks, counting
// down from it, on which the pools differ. Height and Depth are zero when
// the source knows only slots, and the pools are compared at Slot.
type Fork struct {
	Slot   uint64      `json:"slot"`
	Height uint64      `json:"height,omitempty"`
	Depth  uint64      `json:"depth,omitempty"`
	Reason string      `json:"reason"`
	Groups []ForkGroup `json:"groups"`
}

// Node is a pool in the topology graph.
type Node struct {
	ID    string `json:"id"`
	Impl  string `json:"impl"`
	Tier  string `json:"tier"`
	Phase string `json:"phase"`
	Fork  bool   `json:"fork"`
}

// Edge is a peering between two pools, From sorting before To.
type Edge struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// Topology is the graph data for the pool view.
type Topology struct {
	Mode  string `json:"mode"`
	Nodes []Node `json:"nodes"`
	Edges []Edge `json:"edges"`
}

// PoolsView is the Pools view.
type PoolsView struct {
	Pools    []PoolRow `json:"pools"`
	Tips     []ImplTip `json:"tips"`
	Forks    []Fork    `json:"forks"`
	Topology Topology  `json:"topology"`
}

// ImplVerdict is one implementation's answer to a finding's transaction.
type ImplVerdict struct {
	Impl    string `json:"impl"`
	Verdict string `json:"verdict"`
	Build   string `json:"build,omitempty"`
	Dirty   bool   `json:"dirty"`
}

// Finding is an oracle disagreement. No source produces them yet; the shape
// is fixed so the SPA can build against it.
type Finding struct {
	ID            string        `json:"id"`
	Seq           uint64        `json:"seq"`
	Kind          string        `json:"kind"`
	Severity      string        `json:"severity"`
	Summary       string        `json:"summary"`
	TxCBORHex     string        `json:"tx_cbor_hex"`
	Verdicts      []ImplVerdict `json:"verdicts"`
	Reproducible  bool          `json:"reproducible"`
	Provenance    string        `json:"provenance,omitempty"`
	ReplayCommand string        `json:"replay_command,omitempty"`
	// Known is the upstream issue (repo#number) whose defect explains the
	// finding; empty for a finding nothing explains.
	Known string `json:"known,omitempty"`
}

// FindingPage is one page of findings.
type FindingPage struct {
	Total  int       `json:"total"`
	Offset int       `json:"offset"`
	Limit  int       `json:"limit"`
	Rows   []Finding `json:"rows"`
	// KnownTotal and KnownCounts cover every finding, whatever the filter.
	KnownTotal  int            `json:"known_total,omitempty"`
	KnownCounts map[string]int `json:"known_counts,omitempty"`
}

// Decision is one engine decision on the live feed. ID is the log seq, so
// it orders the feed and is the SSE event id. Intent and Scores are set for
// an IntentChosen decision; Body is the decoded record body.
type Decision struct {
	ID      uint64         `json:"id"`
	At      int64          `json:"at"`
	Kind    string         `json:"kind"`
	Actor   uint64         `json:"actor"`
	Profile string         `json:"profile,omitempty"`
	Intent  string         `json:"intent,omitempty"`
	Scores  []agenda.Score `json:"scores,omitempty"`
	Body    any            `json:"body,omitempty"`
}

// Filter selects decisions on the feed. Every set field must match.
type Filter struct {
	Actor   *uint64
	Profile string
	Kinds   []string
	// Intent keeps decisions that chose this intent kind.
	Intent string
	// Vote keeps only the decisions about votes: the vote intents chosen
	// and the votes submitted.
	Vote bool
}

// Match reports whether d passes the filter.
func (f Filter) Match(d Decision) bool {
	if f.Actor != nil && *f.Actor != d.Actor {
		return false
	}
	if f.Profile != "" && f.Profile != d.Profile {
		return false
	}
	if f.Intent != "" && f.Intent != d.Intent {
		return false
	}
	if f.Vote && d.Intent != "vote" {
		return false
	}
	if len(f.Kinds) == 0 {
		return true
	}
	for _, k := range f.Kinds {
		if k == d.Kind {
			return true
		}
	}
	return false
}

// Subscription is one feed client. Replay is what the ring still held after
// the client's Last-Event-ID; Gap reports that the ring had already dropped
// events after it. C delivers live decisions and is closed when the client
// is dropped for falling behind or the feed shuts down. Close releases the
// subscription and is safe to call more than once.
type Subscription struct {
	Replay []Decision
	Gap    bool
	C      <-chan Decision
	Close  func()
}

// withIdentity fills the row's identity columns.
func (r *ActorRow) withIdentity(in store.Identity) {
	r.Kind, r.Address, r.Balance, r.Pending = in.Kind, shortAddr(in.PaymentAddr), in.Settled, in.Unconfirmed
	r.Tokens, r.Delegated, r.Pool, r.DRep = len(in.Tokens), in.DelegatedPool != "", shortAddr(in.DelegatedPool), in.DelegatedDRep
}
