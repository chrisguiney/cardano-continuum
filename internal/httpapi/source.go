package httpapi

import "context"

// Source is the read model the handlers serve. An implementation must be
// safe for concurrent use and must never mutate engine state. A live engine
// supplies its own Source; LogSource is the read-only one over a log file.
type Source interface {
	// Snapshot is the Overview.
	Snapshot(ctx context.Context) (Snapshot, error)
	// Actors pages, sorts and filters the actor table. The handler has
	// already bounded q; an implementation still rejects an unknown sort key
	// with ErrBadRequest.
	Actors(ctx context.Context, q ActorQuery) (ActorPage, error)
	// ActorTimeline is one actor's timeline, or ErrNotFound.
	ActorTimeline(ctx context.Context, id uint64) (Timeline, error)
	// ActorDetail is one actor in full, or ErrNotFound.
	ActorDetail(ctx context.Context, id uint64) (ActorDetail, error)
	// Adversaries is the Adversaries view: each attacker's cost, counters
	// and findings, and the run's impact on honest actors.
	Adversaries(ctx context.Context) (AdversaryView, error)
	// Pools is the Pools view with its topology graph.
	Pools(ctx context.Context) (PoolsView, error)
	// Findings pages oracle findings; none exist before the oracle lands.
	Findings(ctx context.Context, p Page) (FindingPage, error)
	// Subscribe joins the decision feed. after is the client's
	// Last-Event-ID, if it sent one. It returns ErrTooManyClients at the
	// client cap. It must never block the engine: a client that falls
	// behind is disconnected.
	Subscribe(f Filter, after *uint64) (*Subscription, error)
}

// SortKeys are the columns the actor table sorts by.
var SortKeys = []string{"id", "name", "role", "profile", "state", "intents", "txs", "retries", "confirmed", "released", "kind", "balance", "tokens"}
