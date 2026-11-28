// Package judge is the engine's oracle bridge: the mistakes an intent makes,
// the verdicts the node's answers are held to, the invariant monitors, the
// findings they log and the supply checks.
//
//	Sample, Mutate   an intent samples one mistake and injects it into each attempt
//	Judge            a node answer against the verdict set the mistakes call for
//	Watch, Snapshot  the invariant monitors over the engine's state after an input
//	Report           a finding is a decision: only its first occurrence is logged
//
// The composite orders every call. Sample draws from the actor's own stream,
// named by the intent, so the mistake is the same whatever else the actor
// did; nothing else here draws.
//
// Mistakes: an intent samples one mistake from what its first honest
// attempt makes possible (mistake.Applicable, mistake.Sample) and keeps it;
// each attempt carries it less the fixes so far (mistake.Carry), injected
// into the built bytes before the planner reserves the attempt, so the
// reservation names the bytes that are sent.
//
// The oracle: every node answer is judged against mistake.Expect for the
// mistakes the attempt carries (oracle.Judge), and the invariant monitors
// run after every chain input and MonitorSnapshot (oracle.Check). A finding
// is a decision: only the first occurrence of an ID is logged, and replay
// regenerates it from the same inputs.
package judge

import (
	"github.com/chrisguiney/cardano-continuum/internal/build"
	"github.com/chrisguiney/cardano-continuum/internal/chain"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/wire"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/fakechain"
	"github.com/chrisguiney/cardano-continuum/internal/keys"
	"github.com/chrisguiney/cardano-continuum/internal/mistake"
	"github.com/chrisguiney/cardano-continuum/internal/oracle"
	"github.com/chrisguiney/cardano-continuum/internal/policy"
	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/protocol"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
	"github.com/chrisguiney/cardano-continuum/internal/submit"
)

// Roster is the actors as the judge reads them.
type Roster interface {
	Get(id population.ActorID) (population.Actor, bool)
}

// Ring is the keys of the actors that hold pools and DRep credentials.
type Ring interface {
	PoolOf(id population.ActorID) ([28]byte, bool)
	PoolSigner(id population.ActorID) (keys.Signer, error)
	DRepSigner(id population.ActorID) keys.Signer
}

// Chain is the window the judge reads the tip and the settled point from.
type Chain interface {
	Tip() eventlog.ChainPoint
	Confirmed() eventlog.ChainPoint
}

// Journal is the decision log of the input being handled.
type Journal interface {
	Seq() uint64
	Ends() []oracle.End
	Append(rec eventlog.Record)
}

// Ledger is the settled state the supply and the views are checked against.
type Ledger interface {
	// Supply is everything the settled state accounts for.
	Supply() uint64
	// Inflow is what settled reward withdrawals brought in beside the
	// genesis total.
	Inflow() uint64
	// Balances is each actor's settled balance.
	Balances() map[uint64]uint64
	// Holders is the planner ledger's reservations, as the monitors read them.
	Holders() oracle.Holders
}

// Floor is the stake-floor guard's input.
type Floor interface {
	State(at sched.Time) policy.State
}

// Race is the shared outputs an adversary tried to spend.
type Race interface {
	Contested(ref wire.Ref) bool
}

// Env is what the judge shares with the rest of the engine.
type Env struct {
	Roster  Roster
	Ring    Ring
	Chain   Chain
	Journal Journal
	Ledger  Ledger
	Floor   Floor
	Race    Race
	// Forks is the chain view's protocol-version history.
	Forks Forks
}

// Forks is the chain view's protocol-version history, as the protocol
// monitor judges a node's version with it.
type Forks interface {
	// VersionAt is the version the ratified hard forks put in force in
	// epoch ep; ok is false when the view has not reached ep, so a fork
	// ratified meanwhile could still move it.
	VersionAt(ep uint64) (v protocol.Version, ok bool)
}

// Spec is what a Judge is built from.
type Spec struct {
	// Mistakes makes actors err.
	Mistakes bool
	Build    build.Params
	// Run names the run in each finding's replay command.
	Run oracle.Run
	// K is the settled depth the monitors check forks against.
	K          uint64
	BlockEvery uint64
	Guard      policy.Guard
	// Genesis is the lovelace value conservation counts from.
	Genesis uint64
	// Faucet is the faucet's actor, which holds no DRep key.
	Faucet population.ActorID
	// Again are the classes a kind's certificates fail with when the same
	// transaction already landed.
	Again func(kind string) []submit.Class
	// Contended reports a kind whose attempts spend an output other
	// actors race for (a market trade): "inputs spent" is expected from
	// its first send (models/specs/dex-contention, rule 4).
	Contended func(kind string) bool
}

// Judge is the oracle bridge: the mistakes, the monitors and the findings
// they log, over one spec and one environment.
type Judge struct {
	*Mistakes
	*Monitors
	*Log
	spec Spec
	env  Env
	// lost is what settled payments sent to wrong addresses.
	lost uint64
}

// New returns a judge with no findings and nothing lost.
func New(spec Spec, env Env) *Judge {
	l := &Log{set: oracle.NewSet(), jr: env.Journal}
	return &Judge{
		Mistakes: &Mistakes{spec: spec, env: env}, Log: l,
		Monitors: &Monitors{spec: spec, env: env, log: l, monitors: oracle.Monitors(spec.K, spec.Guard)},
		spec:     spec, env: env,
	}
}

// AddLost counts lovelace a settled payment sent to a wrong address.
func (j *Judge) AddLost(n uint64) { j.lost += n }

// Lost is the lovelace settled payments sent to addresses nobody holds.
func (j *Judge) Lost() uint64 { return j.lost }

// Load restores the count from a snapshot; Lost is what one records.
func (j *Judge) Load(n uint64) { j.lost = n }

// Answer compares the node's answers to the current attempt with the
// verdict set its mistakes call for. "Inputs spent" is expected once the
// attempt was sent, or an earlier unfinished attempt it conflicts with may
// be at a node. A DoubleSubmit's second send always follows the first.
func (j *Judge) Answer(at sched.Time, p *wire.Pending, r wire.SubmitResult, out submit.Outcome) error {
	node := oracle.Node{Impl: "node"}
	if r.Node != nil {
		node = *r.Node
	}
	// An adversary racing for the same shared output may have spent it.
	spent := p.M.Sent || len(p.Txs) > 1 || (p.Locked != nil && j.env.Race.Contested(*p.Locked)) ||
		(j.spec.Contended != nil && j.spec.Contended(p.KindOf()))
	want := mistake.WithCollectErrors(Expect(j.spec.Build, p.Carried, spent, j.spec.Again(p.KindOf())), p.Cbor)
	if p.Probe {
		// The probe's honest bytes break only the GOV rule's voter check.
		want.Accept = false
		want = WithClasses(want, submit.Class(fakechain.DisallowedVoters))
	}
	want.Lost = p.Lost
	// A rebuild waits out its backoff before its first send, which can
	// outlast its TTL; the node's mempool validates one slot past its
	// tip, and the engine's tip may trail the node's by a block.
	if p.TTL <= j.env.Chain.Tip().Slot+1+j.spec.BlockEvery {
		want = WithClasses(want, submit.OutsideValidityInterval)
	}
	a := oracle.Attempt{Seq: j.env.Journal.Seq(), Tx: p.Cbor, ID: chain.TxID(p.Tx), Expected: &want, Answers: []oracle.Answer{{Node: node, Outcome: out}}}
	fs := oracle.Judge(a, j.spec.Run)
	if f, ok := oracle.Differential(a, j.spec.Run); ok {
		fs = append(fs, f)
	}
	if r.Again != nil {
		again := mistake.WithCollectErrors(Expect(j.spec.Build, p.Carried, true, j.spec.Again(p.KindOf())), p.Cbor)
		a.Expected, a.Answers = &again, []oracle.Answer{{Node: node, Outcome: AnswerOutcome(*r.Again)}}
		fs = append(fs, oracle.Judge(a, j.spec.Run)...)
	}
	return j.Report(at, fs)
}
