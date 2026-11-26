package scenario

import (
	"errors"
	"fmt"

	"github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/gouroboros/ledger/conway"

	"github.com/chrisguiney/cardano-continuum/internal/fakechain"
	"github.com/chrisguiney/cardano-continuum/internal/mistake"
	"github.com/chrisguiney/cardano-continuum/internal/rng"
	"github.com/chrisguiney/cardano-continuum/internal/tla"
)

// FormatVersion is the on-disk scenario format this package reads and writes.
const FormatVersion = 1

// Source says how a scenario was derived from a model.
type Source string

const (
	// SourceTrace: Mode A, a TLC counterexample of a Bug configuration.
	SourceTrace Source = "trace"
	// SourceVector: a row of the Utxo.tla boundary-transaction table.
	SourceVector Source = "vector"
	// SourceWalk: Mode B, a TLC simulation walk.
	SourceWalk Source = "walk"
	// SourceTable: a cell of a codec model's input table, enumerated here.
	SourceTable Source = "table"
)

// Families with an adapter.
const (
	FamilyUtxo      = "ledger-utxo"
	FamilyWitnesses = "ledger-witnesses"
	FamilyCodec     = "codec"
)

// Provenance says which model text a scenario came from.
type Provenance struct {
	Repo      string `json:"repo"`
	Commit    string `json:"commit"`
	Spec      string `json:"spec"`
	Cfg       string `json:"cfg,omitempty"`
	Invariant string `json:"invariant,omitempty"`
}

// Scenario is one executable case in abstract form: what the model says, not
// yet bytes. Exactly one of Utxo, Witness and Codec is set, by Family.
type Scenario struct {
	Version int        `json:"version"`
	ID      string     `json:"id"`
	Family  string     `json:"family"`
	Source  Source     `json:"source"`
	Prov    Provenance `json:"provenance"`

	Utxo    *UtxoCase    `json:"utxo,omitempty"`
	Witness *WitnessCase `json:"witness,omitempty"`
	Codec   *CodecCase   `json:"codec,omitempty"`
}

// UtxoCase is a ledger of Utxo.tla and the transactions offered to it in
// order, each with the reference's verdict.
type UtxoCase struct {
	Era      string        `json:"era"`
	Mutation string        `json:"mutation,omitempty"`
	Start    tla.Ledger    `json:"start"`
	Steps    []tla.Verdict `json:"steps"`
}

// Caps are the features of a target that decide whether a step can run there.
type Caps struct {
	// Rewards: reward accounts can hold a balance before the scenario.
	Rewards bool
	// Phase2Invalid: a transaction with IsValid false can be submitted.
	Phase2Invalid bool
}

// Env is what concretizing needs from the run and the target.
type Env struct {
	// Seed derives the scenario's keys.
	Seed      rng.Seed
	NetworkID uint8
	// Params are the target's protocol parameters before the scenario's
	// own requirements (Preconditions.Params) are applied.
	Params conway.ConwayProtocolParameters
	Caps   Caps
	// Start is the slot the target validates the first submission at.
	Start uint64
}

// Preconditions is what a target must provide before a scenario's steps run.
type Preconditions struct {
	// Era is the ledger era the steps are written for.
	Era string
	// ProtocolMajor is the protocol version the verdicts assume.
	ProtocolMajor uint
	// Params are the protocol parameters with the scenario's requirements
	// applied: the refinement mapping fixes the fee, the deposit and the
	// collateral rule so the model's unit boundaries fall on real ones.
	Params conway.ConwayProtocolParameters
	// Funder receives Funds lovelace in one output before the first step.
	Funder common.Address
	Funds  uint64
	// UTxOs is how many outputs the scenario reserves for its own use.
	UTxOs int
	// Pools is how many registered pools the scenario needs.
	Pools int
}

// StepKind is what a concrete step does.
type StepKind string

const (
	// Submit sends a transaction and judges the answer.
	Submit StepKind = "submit"
	// Wait advances the target to a slot.
	Wait StepKind = "wait"
	// Fault injects an environment fault into a fake chain.
	Fault StepKind = "fault"
)

// Step is one concrete action against a target.
type Step struct {
	Kind StepKind
	// Note says what the step is for, in the model's terms.
	Note string
	// Index is the abstract step this realises, -1 for a precondition.
	Index int

	// Tx and ID are the transaction of a Submit.
	Tx []byte
	ID common.Blake2b256
	// Expect is the model's verdict as an oracle expectation.
	Expect mistake.Expected
	// Strict makes a rejection that names a failure outside Expect a
	// disagreement too. The codec models' verdict is whether the bytes
	// decode, so a ledger-rule rejection of bytes the reference cannot
	// decode means the node decoded them.
	Strict bool
	// Setup marks a precondition transaction: it must be accepted, and its
	// rejection is a failure to concretize, not a finding.
	Setup bool
	// Slot is the slot a Submit must be validated at, or a Wait must reach.
	Slot uint64

	Fault fakechain.Fault
}

// Skip records an abstract step that was not concretized.
type Skip struct {
	Index  int
	Reason string
}

// Plan is a concretized scenario.
type Plan struct {
	Steps []Step
	// Skipped are rejected steps left out: they change no state, so the
	// steps after them still run.
	Skipped []Skip
	// Truncated, when set, is the accepted step the plan stops before:
	// every later step depends on the state it would have made.
	Truncated *Skip
}

// Adapter is the refinement mapping of one model family: it turns abstract
// model state into concrete actions and the model's verdicts into oracle
// expectations.
type Adapter interface {
	Family() string
	// Applicable reports why a scenario cannot be concretized at all, as an
	// *Unmappable, or nil.
	Applicable(s Scenario, env Env) error
	// Preconditions is what the target must provide.
	Preconditions(s Scenario, env Env) (Preconditions, error)
	// Concretize returns the concrete steps. env.Start must be the slot the
	// target validates at once it meets the preconditions.
	Concretize(s Scenario, env Env) (Plan, error)
	// Expected maps the model's verdict on abstract step i to what a node
	// must answer.
	Expected(s Scenario, i int) mistake.Expected
}

// Unmappable reports model state the refinement mapping has no concrete form
// for. It is never dropped silently: the scenario, or the step, is reported
// as unconcretizable with Reason.
type Unmappable struct {
	// Step is the abstract step, or -1 for the whole scenario.
	Step   int
	Reason string
}

func (u *Unmappable) Error() string {
	if u.Step < 0 {
		return "unconcretizable: " + u.Reason
	}
	return fmt.Sprintf("unconcretizable step %d: %s", u.Step, u.Reason)
}

func unmappable(step int, format string, args ...any) *Unmappable {
	return &Unmappable{Step: step, Reason: fmt.Sprintf(format, args...)}
}

// ErrNoAdapter reports a scenario of a family nothing here concretizes.
var ErrNoAdapter = errors.New("scenario: no adapter for family")

// Adapters returns the adapter of every family, in a fixed order.
func Adapters() []Adapter {
	return []Adapter{NewUtxoAdapter(), NewWitnessAdapter(), NewCodecAdapter()}
}

// AdapterFor returns the adapter of s's family.
func AdapterFor(s Scenario) (Adapter, error) {
	for _, a := range Adapters() {
		if a.Family() == s.Family {
			return a, nil
		}
	}
	return nil, fmt.Errorf("%w %q", ErrNoAdapter, s.Family)
}
