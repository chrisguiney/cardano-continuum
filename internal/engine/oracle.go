package engine

import (
	"bytes"
	"errors"
	"fmt"
	"slices"
	"strconv"

	"github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/gouroboros/ledger/conway"
	"github.com/blinklabs-io/gouroboros/ledger/shelley"

	"github.com/chrisguiney/cardano-continuum/internal/build"
	"github.com/chrisguiney/cardano-continuum/internal/chain"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/fakechain"
	"github.com/chrisguiney/cardano-continuum/internal/keys"
	"github.com/chrisguiney/cardano-continuum/internal/mistake"
	"github.com/chrisguiney/cardano-continuum/internal/oracle"
	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
	"github.com/chrisguiney/cardano-continuum/internal/submit"
)

// This file wires internal/mistake and internal/oracle into the engine.
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

// sample draws the intent's mistake from its first honest attempt. The
// stream is the actor's, named by the intent, so it is the same draw
// whatever else the actor did.
func (e *Engine) sample(p *pendingRec, w keys.Wallet, built build.Built) {
	if !e.cfg.Mistakes {
		return
	}
	a, _ := e.census.Get(population.ActorID(p.Actor))
	t := profile.SampledTraits{Competence: a.Competence, Care: a.Traits.Care}
	// The engine's scripts (build.AlwaysSucceeds, build.FreeMint) accept
	// any redeemer, so a wrong one is no mistake: the node rightly
	// accepts it. Withheld mistakes cannot run yet (mistake.Withheld).
	ok := mistake.Applicable(built.Tx, e.mistakeCtx(p, w)).Without(withheldKinds)
	p.Injected = mistake.Sample(t, ok, a.Stream("mistake", strconv.FormatUint(p.ID, 10)))
}

// withheldKinds are the mistakes the engine never samples.
var withheldKinds = func() mistake.KindSet {
	s := mistake.Of(mistake.WrongDatum)
	for _, k := range mistake.Kinds() {
		if mistake.Withheld(k) != "" {
			s |= mistake.Of(k)
		}
	}
	return s
}()

// mutate makes the mistakes the attempt carries in the honest built. A
// mistake that this attempt's shape does not allow (its rebuild lost the
// change output, say) is left out of it, and Carried says so.
func (e *Engine) mutate(p *pendingRec, w keys.Wallet, built build.Built) (build.Built, error) {
	p.Carried, p.Lost = mistake.Carry(p.Injected, p.M.Fixed), 0
	if p.Carried == 0 {
		return built, nil
	}
	tx, want, err := mistake.Inject(built.Tx, p.Carried, e.mistakeCtx(p, w))
	if errors.Is(err, mistake.ErrNotApplicable) || errors.Is(err, mistake.ErrNoSigner) {
		p.Carried = 0
		return built, nil
	}
	if err != nil {
		return build.Built{}, fmt.Errorf("engine: intent %d: inject %v: %w", p.ID, p.Carried, err)
	}
	t, err := conway.NewConwayTransactionFromCbor(tx)
	if err != nil {
		return build.Built{}, fmt.Errorf("engine: intent %d: decode mutant: %w", p.ID, err)
	}
	built.Tx, built.ID, p.Lost = tx, t.Hash(), want.Lost
	return built, nil
}

// mistakeCtx is what Inject needs to re-sign an edited body: every key the
// actor's transactions are signed with.
func (e *Engine) mistakeCtx(p *pendingRec, w keys.Wallet) mistake.Ctx {
	id := population.ActorID(p.Actor)
	signers := []keys.Signer{w.PaymentSigner(), w.StakeSigner()}
	if id != faucetActor {
		signers = append(signers, e.keys.DRepSigner(keysID(id)))
	}
	if _, ok := e.poolOf(id); ok {
		if s, err := e.poolSigner(id); err == nil {
			signers = append(signers, s)
		}
	}
	return mistake.Ctx{Params: e.cfg.Build, Signers: signers, Change: w.Address(), Slot: e.Tip().Slot, Stale: &staleInput}
}

// staleInput is the output a StaleInput mistake spends: one that never
// existed, which the ledger answers like a spent one. Its ID is the largest
// there is, so it sorts after every real input and the redeemers' input
// indices still point at the script inputs; an input sorting before one
// would also make the node report the redeemers' pointers wrong.
var staleInput = shelley.ShelleyTransactionInput{TxId: common.Blake2b256(bytes.Repeat([]byte{0xff}, 32))}

// diagnoser is who reads a rejection: with mistakes on, the actor at its
// competence; otherwise the configured one.
func (e *Engine) diagnoser(a population.Actor) submit.Diagnoser {
	if e.cfg.Mistakes && e.cfg.Diagnoser == nil {
		return mistake.Diagnoser{Competence: a.Competence}
	}
	return e.diag
}

// judge compares the node's answers to the current attempt with the
// verdict set its mistakes call for. "Inputs spent" is expected once the
// attempt was sent, or an earlier unfinished attempt it conflicts with may
// be at a node. A DoubleSubmit's second send always follows the first.
func (e *Engine) judge(at sched.Time, p *pendingRec, r SubmitResult, out submit.Outcome) error {
	node := oracle.Node{Impl: "node"}
	if r.Node != nil {
		node = *r.Node
	}
	// An adversary racing for the same shared output may have spent it.
	spent := p.M.Sent || len(p.Txs) > 1 || (p.Locked != nil && e.contested[*p.Locked])
	want := expect(e.cfg.Build, p.Carried, spent, specs[p.kindOf()].again)
	if p.Probe {
		// The probe's honest bytes break only the GOV rule's voter check.
		want.Accept = false
		want = withClasses(want, submit.Class(fakechain.DisallowedVoters))
	}
	want.Lost = p.Lost
	// A rebuild waits out its backoff before its first send, which can
	// outlast its TTL; the node's mempool validates one slot past its
	// tip, and the engine's tip may trail the node's by a block.
	if p.TTL <= e.Tip().Slot+1+e.cfg.BlockEvery {
		want = withClasses(want, submit.OutsideValidityInterval)
	}
	a := oracle.Attempt{Seq: e.jr.Seq(), Tx: p.Cbor, ID: chain.TxID(p.Tx), Expected: &want, Answers: []oracle.Answer{{Node: node, Outcome: out}}}
	fs := oracle.Judge(a, e.cfg.Run)
	if f, ok := oracle.Differential(a, e.cfg.Run); ok {
		fs = append(fs, f)
	}
	if r.Again != nil {
		again := expect(e.cfg.Build, p.Carried, true, specs[p.kindOf()].again)
		a.Expected, a.Answers = &again, []oracle.Answer{{Node: node, Outcome: answerOutcome(*r.Again)}}
		fs = append(fs, oracle.Judge(a, e.cfg.Run)...)
	}
	return e.report(at, fs)
}

// expect is mistake.ExpectOn the run's parameters, plus the classes a node names beside "inputs
// spent" when the attempt's own earlier copy already landed: its
// certificates are applied, so applying them again fails their rule too
// (cardano-ledger runs the CERTS rule on the inputs it still holds and
// accumulates both failures, as it does ValueNotConserved).
func expect(pp build.Params, ks mistake.KindSet, spent bool, again []submit.Class) mistake.Expected {
	e := mistake.ExpectOn(ks, spent, pp)
	if !spent {
		return e
	}
	return withClasses(e, again...)
}

// withClasses adds cs to e's classes, which Allows needs sorted.
func withClasses(e mistake.Expected, cs ...submit.Class) mistake.Expected {
	if len(cs) == 0 {
		return e
	}
	e.Classes = append(slices.Clone(e.Classes), cs...)
	slices.Sort(e.Classes)
	e.Classes = slices.Compact(e.Classes)
	return e
}

func answerOutcome(a Answer) submit.Outcome {
	o := submit.Outcome{Verdict: submit.Unreached}
	switch a.Verdict {
	case VerdictAccepted:
		o.Verdict = submit.Accepted
	case VerdictRejected:
		o.Verdict = submit.Rejected
		for _, c := range a.Failures {
			o.Failures = append(o.Failures, submit.Failure{Class: submit.Class(c)})
		}
	}
	return o
}

// watch runs the invariant monitors over the engine's state after an input:
// value, the settled point's move since before it (from), the intents that
// ended in it, the reservations and the stake floor, plus whatever the
// nodes answered (views).
func (e *Engine) watch(at sched.Time, from eventlog.ChainPoint, views []oracle.ViewCheck, tips []oracle.Branch) error {
	before, _ := chain.PointFromLog(from)
	conf, _ := chain.PointFromLog(e.conf)
	res := oracle.ReservationsOf(e.led)
	s := oracle.Snapshot{
		Seq: e.jr.Seq(), Run: e.cfg.Run,
		Value:        &oracle.Value{Genesis: e.genesis, Utxo: e.Supply(), At: conf},
		Ends:         e.jr.Ends(),
		Reservations: &res,
		Settled:      &oracle.SettledMove{From: before, To: conf},
		Views:        views,
		Tips:         tips,
		Budget:       e.budget,
	}
	// Without pools there is no forging stake to hold a floor: the fake
	// chain forges anyway, so the floor and liveness are not evaluable.
	if pol := e.floorState(at); len(pol.Pools) > 0 {
		s.Policy = &pol
	}
	return e.report(at, oracle.Check(s, e.monitors))
}

// blockBudget sums the execution units a block's transactions declare, for
// the block-budget monitor; nil when the run has no block budget.
func (e *Engine) blockBudget(at eventlog.ChainPoint, txs []TxDigest) *oracle.BlockBudget {
	max := e.cfg.Build.MaxBlockExUnits
	if max.Memory == 0 && max.Steps == 0 {
		return nil
	}
	p, _ := chain.PointFromLog(at)
	b := &oracle.BlockBudget{At: p, Max: max, Txs: len(txs)}
	for _, tx := range txs {
		if tx.Units != nil {
			b.Used.Memory += tx.Units.Memory
			b.Used.Steps += tx.Units.Steps
		}
	}
	return b
}

// monitorSnapshot fills in the engine's side of each view the runner
// asked a node for: the settled balances, at the settled point.
func (e *Engine) monitorSnapshot(at sched.Time, from eventlog.ChainPoint, m MonitorSnapshot) error {
	conf, _ := chain.PointFromLog(e.conf)
	bal := map[population.ActorID]uint64{}
	for id, v := range e.Balances() {
		bal[population.ActorID(id)] = v
	}
	for i := range m.Views {
		m.Views[i].At, m.Views[i].Engine = conf, bal
	}
	return e.watch(at, from, m.Views, m.Tips)
}

// report logs each finding the set has not seen.
func (e *Engine) report(at sched.Time, fs []oracle.Finding) error {
	for _, f := range e.findings.Add(fs...) {
		rec, err := f.Record(at)
		if err != nil {
			return err
		}
		e.jr.Append(rec)
	}
	return nil
}

// Findings is the set of findings this engine has logged. It is safe for
// concurrent use, so an API may serve it while the loop runs.
func (e *Engine) Findings() *oracle.Set { return e.findings }
