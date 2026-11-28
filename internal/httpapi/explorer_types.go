package httpapi

import (
	"github.com/chrisguiney/cardano-continuum/internal/contract"
	"github.com/chrisguiney/cardano-continuum/internal/explorer"
)

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
	// Validator names the script the transaction pays to or runs.
	Validator string `json:"validator,omitempty"`
	// Scripts are the transaction's script runs as the engine evaluated
	// the bytes it submitted: the units used, beside those declared.
	Scripts []contract.Run `json:"scripts,omitempty"`
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

// ContractsView is the Contracts page: every registry validator, with what
// the run's log says of its runs.
type ContractsView struct {
	// Toolchain is the compiler that built the registry.
	Toolchain string `json:"toolchain"`
	// Validators are the registry's validators by name.
	Validators []ValidatorRow `json:"validators"`
}

// ValidatorRow is one validator and its runs.
type ValidatorRow struct {
	// Name is the validator's name (contract.Validator).
	Name string `json:"name"`
	// Module is its Aiken source file.
	Module string `json:"module"`
	// Hash is the unapplied script hash in hex.
	Hash string `json:"hash"`
	// Size is the script's size in bytes.
	Size int `json:"size"`
	// Language is "plutus_v1", "plutus_v2" or "plutus_v3".
	Language string `json:"language"`
	// Purposes are the purposes it has handlers for.
	Purposes []string `json:"purposes"`
	// Params name its parameters.
	Params []string `json:"params,omitempty"`
	// Datum names its datum type.
	Datum string `json:"datum,omitempty"`
	// Redeemer names its redeemer type.
	Redeemer string `json:"redeemer,omitempty"`
	// Instances counts the distinct instances deployed or run.
	Instances int `json:"instances"`
	// Runs counts script runs of submitted transactions, a resend once.
	Runs int `json:"runs"`
	// Failures counts the runs that did not pass.
	Failures int `json:"failures"`
	// Used summarises the units the runs spent.
	Used UnitStats `json:"used"`
	// Declared summarises the units the runs' redeemers declared.
	Declared UnitStats `json:"declared"`
	// Reasons count the failures by reason, most frequent first.
	Reasons []Reason `json:"reasons"`
	// Last are the newest runs, newest first.
	Last []RecentRun `json:"last"`
}

// UnitStats are the average and peak of a set of execution units.
type UnitStats struct {
	// AvgMem is the mean memory units, rounded down.
	AvgMem int64 `json:"avg_mem"`
	// AvgSteps is the mean CPU steps, rounded down.
	AvgSteps int64 `json:"avg_steps"`
	// MaxMem is the peak memory units.
	MaxMem int64 `json:"max_mem"`
	// MaxSteps is the peak CPU steps.
	MaxSteps int64 `json:"max_steps"`
}

// Reason is one failure reason and how often it occurred.
type Reason struct {
	// Reason is the evaluator's error.
	Reason string `json:"reason"`
	// Count is how many runs failed with it.
	Count int `json:"count"`
}

// RecentRun is one run, with the transaction that asked for it.
type RecentRun struct {
	// Tx is the transaction id in hex.
	Tx string `json:"tx"`
	// At is the simulated time it was submitted, in ms.
	At int64 `json:"at"`
	// Instance is the deployed instance, empty for an unparameterised validator.
	Instance string `json:"instance,omitempty"`
	// Purpose is the redeemer's purpose.
	Purpose string `json:"purpose"`
	// OK reports that it passed.
	OK bool `json:"ok"`
	// Used are the units evaluation spent.
	Used contract.Units `json:"used"`
	// Declared are the units the redeemer declared.
	Declared contract.Units `json:"declared"`
	// Error is why it failed.
	Error string `json:"error,omitempty"`
}
