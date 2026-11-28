package httpapi

// Test-only names for what moved to view and stream, so the tests read as before.

import (
	"github.com/chrisguiney/cardano-continuum/internal/httpapi/stream"
	"github.com/chrisguiney/cardano-continuum/internal/httpapi/view"
)

type (
	Activity        = view.Activity
	ActorDetail     = view.ActorDetail
	ActorFilter     = view.ActorFilter
	ActorFinding    = view.ActorFinding
	ActorPage       = view.ActorPage
	ActorQuery      = view.ActorQuery
	ActorRow        = view.ActorRow
	Ada             = view.Ada
	AdversaryRow    = view.AdversaryRow
	AdversaryView   = view.AdversaryView
	AttackStepRow   = view.AttackStepRow
	ControlState    = view.ControlState
	CostView        = view.CostView
	Decision        = view.Decision
	Delegation      = view.Delegation
	Edge            = view.Edge
	Enums           = view.Enums
	EpochCost       = view.EpochCost
	Filter          = view.Filter
	Finding         = view.Finding
	FindingPage     = view.FindingPage
	Fork            = view.Fork
	GovernanceView  = view.GovernanceView
	Holdings        = view.Holdings
	Hub             = stream.Hub
	ImpactRow       = view.ImpactRow
	ImplTip         = view.ImplTip
	Invariant       = view.Invariant
	Lifecycle       = view.Lifecycle
	LogSource       = view.LogSource
	LogSourceConfig = view.LogSourceConfig
	Node            = view.Node
	Page            = view.Page
	Point           = view.Point
	NodeRow         = view.NodeRow
	PoolVotes       = view.PoolVotes
	NodesView       = view.NodesView
	StakePoolRow    = view.StakePoolRow
	StakePoolsView  = view.StakePoolsView
	Population      = view.Population
	Projection      = view.Projection
	ProposalDetail  = view.ProposalDetail
	ProposalRow     = view.ProposalRow
	Snapshot        = view.Snapshot
	Subscription    = view.Subscription
	SuccessView     = view.SuccessView
	Tail            = stream.Tail
	Tally           = view.Tally
	Timeline        = view.Timeline
	TimelineEntry   = view.TimelineEntry
	Topology        = view.Topology
	Traits          = view.Traits
	TxCounts        = view.TxCounts
	VoteRow         = view.VoteRow
	WalletIDs       = view.WalletIDs
)

const (
	DefaultLimit = view.DefaultLimit
	MaxLimit     = view.MaxLimit
)

var (
	ErrBadRequest = view.ErrBadRequest
	ErrNotFound   = view.ErrNotFound
	NewFeed       = stream.NewFeed
	NewHub        = stream.NewHub
	NewLogSource  = view.NewLogSource
	NewProjection = view.NewProjection
	NewTail       = stream.NewTail
)
