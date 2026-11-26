package httpapi

import "github.com/chrisguiney/cardano-continuum/internal/explorer"

// ActorRef names the continuum actor that owns an address.
type ActorRef struct {
	ID   uint64 `json:"id"`
	Name string `json:"name"`
	Role string `json:"role"`
}

// BlockDetail is a block with where each pool's chain stands against it.
// Stub is set for a block the index never saw, known only from a pool's tip.
type BlockDetail struct {
	explorer.BlockView
	Stub bool `json:"stub"`
	// Pools says, for each followed pool, whether it has this block; Fork is
	// true when any pool has a different block at the same height.
	Pools []explorer.Placed `json:"pools"`
	Fork  bool              `json:"fork"`
}

// TxFinding is an oracle finding about a transaction.
type TxFinding struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	Severity string `json:"severity"`
	Summary  string `json:"summary"`
}

// TxContext is what the run knows about a transaction that the chain does
// not: which actor submitted it and why, and what the oracle made of it.
type TxContext struct {
	Actor  *ActorRef `json:"actor,omitempty"`
	Intent uint64    `json:"intent"`
	// Kind is the intent kind; empty for a payment.
	Kind    string `json:"kind,omitempty"`
	Amount  uint64 `json:"amount"`
	Attack  string `json:"attack,omitempty"`
	Verdict string `json:"verdict,omitempty"`
	Reason  string `json:"reason,omitempty"`
	// Findings are the oracle findings that name the transaction.
	Findings []TxFinding `json:"findings"`
}

// TxDetail is a transaction with its continuum context, nil when the run did
// not submit it.
type TxDetail struct {
	explorer.TxView
	Continuum *TxContext `json:"continuum,omitempty"`
}

// AddressDetail is an address with the actor that owns it.
type AddressDetail struct {
	explorer.AddressView
	Actor *ActorRef `json:"actor,omitempty"`
	// StakeActor is the actor that owns the stake address.
	StakeActor *ActorRef `json:"stake_actor,omitempty"`
}

// SearchResult is where a search key leads: Route is the UI path.
type SearchResult struct {
	Kind  explorer.Kind `json:"kind"`
	ID    string        `json:"id"`
	Route string        `json:"route"`
}

// ExplorerStatus says whether the index has anything to show.
type ExplorerStatus struct {
	Blocks     int    `json:"blocks"`
	FullBlocks int    `json:"full_blocks"`
	Tip        uint64 `json:"tip_height"`
	// Skipped counts BlockApplied records that carried no raw block, and
	// Failed those the index refused.
	Skipped uint64 `json:"skipped"`
	Failed  uint64 `json:"failed"`
}
