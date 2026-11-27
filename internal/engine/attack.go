package engine

import (
	"bytes"
	"cmp"
	"context"
	"fmt"
	"math/rand/v2"
	"slices"

	"github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/gouroboros/ledger/conway"

	"github.com/chrisguiney/cardano-continuum/internal/adversary"
	"github.com/chrisguiney/cardano-continuum/internal/agenda"
	"github.com/chrisguiney/cardano-continuum/internal/build"
	"github.com/chrisguiney/cardano-continuum/internal/chain"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/intent"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/pay"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/wire"
	"github.com/chrisguiney/cardano-continuum/internal/keys"
	"github.com/chrisguiney/cardano-continuum/internal/mistake"
	"github.com/chrisguiney/cardano-continuum/internal/oracle"
	"github.com/chrisguiney/cardano-continuum/internal/plan"
	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
	"github.com/chrisguiney/cardano-continuum/internal/submit"
)

// This file wires internal/adversary into the engine. An adversary is an
// actor whose profile has an adversary block: register arms its agenda
// with the attacks (agenda.WithAttacks), act hands a chosen attack to
// adversary.Plan, and strike builds, charges, reserves and logs the step.
// front-run has no turn of its own: start hands every honest unlock to
// react, and the front-runner's outbidding unlock is sent ahead of it.
//
// An attack is one-shot, like a refill: no retry machine drives it, a
// refused attempt is released, and every answer is judged against the
// verdict its step calls for (malformed ones: never an acceptance).
//
// Nothing here runs unless Config.Adversaries is cleared (New refuses an
// adversary actor otherwise), and every lovelace spent is charged against
// the actor's cost ceiling for the epoch before anything is reserved.

// coverTraffic marks, in Submitted.Attack, an adversary's ordinary intent.
const coverTraffic = "cover"

// dustKind is a dust flood's spec key.
const dustKind = "dust"

// dustGuess sizes a dust output for the first build, which only measures
// the outputs' encoded size; the second build pays each its min-UTxO.
const dustGuess = 1_000_000

// maxVictims bounds the honest outputs a steal chooses from.
const maxVictims = 256

// wantRec is the verdict an attack's first send calls for
// (mistake.Expected).
type wantRec struct {
	Classes []string `cbor:"1,keyasint,omitempty"`
	Accept  bool     `cbor:"2,keyasint,omitempty"`
}

func (c *Config) spawns(name string, p profile.Profile) bool {
	if p.Hazards.Birth > 0 {
		return true
	}
	return slices.ContainsFunc(c.Cohorts, func(co Cohort) bool { return co.Profile == name && co.Count > 0 })
}

// arm returns pol with the actor's attacks, when the profile has them, and
// opens its cost account. checkAdversaries has vetted the profile.
func (e *Engine) arm(id population.ActorID, p profile.Profile, pol agenda.Policy) agenda.Policy {
	if p.Adversary == nil {
		return pol
	}
	ap, err := adversary.NewPolicy(*p.Adversary)
	if err != nil {
		return pol
	}
	armed, err := agenda.WithAttacks(p.Agenda, ap)
	if err != nil {
		return pol
	}
	e.advs[id] = ap
	e.costs.Limit(id, uint64(p.Adversary.CostCeiling))
	return armed
}

func (e *Engine) isAdversary(id population.ActorID) bool {
	_, ok := e.advs[id]
	return ok
}

// attack plans and makes the attack the adversary chose.
func (e *Engine) attack(ctx context.Context, at sched.Time, a population.Actor, in agenda.Intent, r *rand.Rand) error {
	n, _ := adversary.Of(in.Kind)
	step, why := adversary.Plan(n, view{e: e}, r)
	if why == "" {
		var err error
		if why, err = e.strike(ctx, at, a.ID, step, 0); err != nil {
			return err
		}
	}
	if why != "" {
		e.jr.Emit(at, KindSkipped, a.ID, Skipped{Kind: string(n), Why: why})
	}
	return nil
}

// react gives every front-runner the honest unlock p, built with fee, to
// outbid; the first that can afford it does.
// checkAdversaries refuses an adversary profile that is malformed, and one
// the run would spawn without a devnet clearance.
func (c *Config) checkAdversaries() error {
	for _, n := range c.Profiles.Names() {
		p, _ := c.Profiles.Get(n)
		if p.Adversary == nil {
			continue
		}
		pol, err := adversary.NewPolicy(*p.Adversary)
		if err == nil {
			_, err = agenda.WithAttacks(p.Agenda, pol)
		}
		if err != nil {
			return fmt.Errorf("%w: profile %q: %w", ErrConfig, n, err)
		}
		if c.spawns(n, p) && !c.Adversaries.Cleared() {
			return fmt.Errorf("engine: profile %q: %w", n, adversary.ErrNotDevnet)
		}
	}
	return nil
}

func (e *Engine) react(ctx context.Context, at sched.Time, p *wire.Pending, fee uint64) error {
	if p.Kind != unlockKind || p.Attack != "" || p.Locked == nil || len(e.advs) == 0 {
		return nil
	}
	target := adversary.Locked{
		Ref:   adversary.Ref{Tx: p.Locked.Tx, Index: p.Locked.Index},
		Owner: population.ActorID(p.Actor), Lovelace: p.Amount, Busy: true,
	}
	for _, id := range e.co.IDs() {
		if pol := e.advs[id]; !pol.Has(adversary.FrontRun) || e.abandoned(id) {
			continue
		}
		why, err := e.strike(ctx, at, id, adversary.Outbid(target, fee), p.ID)
		if err != nil || why == "" {
			return err
		}
		e.jr.Emit(at, KindSkipped, id, Skipped{Kind: string(adversary.FrontRun), Why: why})
	}
	return nil
}

func (e *Engine) abandoned(id population.ActorID) bool {
	a, ok := e.co.Get(id)
	return !ok || a.State == population.Abandoned
}

// shot is one attack step built: its transactions (the first is sent
// first, the rest are rivals on the same inputs), what they spend and
// cost, and the record the pending intent starts from.
type shot struct {
	txs      []build.Built
	inputs   []Ref
	ttl      uint64
	cost     uint64
	want     mistake.Expected
	rec      wire.Pending
	payees   []uint64
	mutation string
}

// strike builds step for adversary id and, when the cost fits its ceiling,
// reserves and logs it. against is the honest intent a front-run outbids.
// It returns why the step was not made, if it was not.
func (e *Engine) strike(ctx context.Context, at sched.Time, id population.ActorID, step adversary.Step, against uint64) (string, error) {
	iid := e.pay.Next()
	s, why, err := e.build(ctx, at, id, iid, step)
	if why != "" || err != nil {
		return why, err
	}
	epoch := e.cfg.Slots.EpochAt(at)
	if left := e.costs.Remaining(id, epoch); s.cost > left {
		return fmt.Sprintf("cost ceiling: step costs %d, %d left this epoch", s.cost, left), nil
	}
	for _, b := range s.txs {
		if err := e.pay.Reserve(ctx, iid, id, TxID(b.ID), s.inputs, s.ttl); err != nil {
			return "reserve: " + err.Error(), nil
		}
	}
	if err := e.costs.Charge(id, epoch, s.cost); err != nil {
		return "", fmt.Errorf("engine: charge attack %d: %w", iid, err)
	}
	e.pay.Claim(iid)
	p := s.rec
	p.ID, p.Actor, p.Attack, p.Want = iid, uint64(id), string(step.Attack), wire.WantOf(s.want)
	p.Tx, p.TTL, p.Cbor, p.Awaiting, p.Jump = TxID(s.txs[0].ID), s.ttl, s.txs[0].Tx, true, against != 0
	body := Attack{Intent: iid, Name: string(step.Attack), Mutation: s.mutation, Cost: s.cost, Against: against, Payees: s.payees, Spend: p.Locked}
	for _, b := range s.txs {
		p.Txs = append(p.Txs, TxID(b.ID))
		body.Txs = append(body.Txs, TxID(b.ID))
	}
	for _, b := range s.txs[1:] {
		p.Rivals = append(p.Rivals, Rival{ID: TxID(b.ID), Tx: b.Tx})
	}
	if p.Locked != nil {
		e.contested[*p.Locked] = true
		body.Value = p.Amount
	}
	e.pay.Add(&p)
	e.pc.Excite(at)
	e.jr.Emit(at, KindAttack, id, body)
	for _, b := range s.txs {
		e.in.Submitted(at, &p, b, s.inputs)
	}
	return "", nil
}

// build makes step's transactions for adversary id.
func (e *Engine) build(ctx context.Context, at sched.Time, id population.ActorID, iid uint64, step adversary.Step) (shot, string, error) {
	var s shot
	var why string
	var err error
	switch step.Attack {
	case adversary.DustFlood:
		s, why, err = e.dust(ctx, at, id, iid, step)
	case adversary.Contention, adversary.FrontRun:
		s, why, err = e.grab(ctx, at, id, iid, step)
	case adversary.Malformed:
		s, why, err = e.malformed(ctx, at, id, iid, step)
	case adversary.DoubleSpend:
		s, why, err = e.race(ctx, at, id, iid, step)
	case adversary.ExUnitsGap:
		s, why, err = e.gap(ctx, at, id, iid, step)
	default:
		return shot{}, "no builder for " + string(step.Attack), nil
	}
	if err != nil && ctx.Err() != nil {
		return shot{}, "", ctx.Err()
	}
	if err != nil {
		return shot{}, "build: " + err.Error(), nil
	}
	return s, why, nil
}

// fund selects the adversary's inputs for need lovelace.
func (e *Engine) fund(id population.ActorID, iid uint64, need uint64) ([]Coin, string) {
	avail := e.pay.Coins(id, plan.IntentID(iid))
	coins := e.sel.Select(avail, need)
	if coins == nil {
		return nil, fmt.Sprintf("spendable %d below %d", pay.SumCoins(avail), need)
	}
	return coins, ""
}

func refs(coins []Coin) []Ref {
	out := make([]Ref, len(coins))
	for i, c := range coins {
		out[i] = c.Ref
	}
	return out
}

// dust pays every payee one output of exactly its min-UTxO.
func (e *Engine) dust(ctx context.Context, at sched.Time, id population.ActorID, iid uint64, step adversary.Step) (shot, string, error) {
	outs := make([]build.Output, len(step.Payees))
	s := shot{rec: wire.Pending{Args: wire.Args{Kind: dustKind}}, want: mistake.Expect(0, false), ttl: e.in.TTL(at, false, false)}
	for i, p := range step.Payees {
		outs[i] = build.Output{To: build.AlwaysSucceeds.Address(e.cfg.NetworkID), Amount: dustGuess}
		if !p.Script {
			outs[i].To = e.co.Wallet(p.Actor).Address()
		}
		s.payees = append(s.payees, uint64(p.Actor))
	}
	coins, why := e.fund(id, iid, uint64(len(outs))*dustGuess*2+feeHeadroom)
	if why != "" {
		return shot{}, why, nil
	}
	w := e.co.Wallet(id)
	sp := build.Spend{Inputs: e.ledgerUtxos(w, coins), TTL: s.ttl}
	b, err := e.builder.FanOut(ctx, w, sp, outs)
	// A min-UTxO depends on the output's encoded size, which depends on
	// its coin: rebuild until the amounts are the minimum of themselves.
	for pass := 0; err == nil && pass < 3; pass++ {
		var changed bool
		if changed, err = e.atMinimum(b.Tx, outs); err != nil || !changed {
			break
		}
		b, err = e.builder.FanOut(ctx, w, sp, outs)
	}
	if err != nil {
		return shot{}, "", err
	}
	s.txs, s.inputs, s.cost = []build.Built{b}, refs(coins), b.Fee
	for _, o := range outs {
		s.cost += o.Amount
		s.rec.Amount += o.Amount
	}
	return s, "", nil
}

// atMinimum sets each of outs to the min-UTxO of its output in tx, the
// Babbage rule (160 + encoded size) * coinsPerUTxOByte, and reports
// whether any changed.
func (e *Engine) atMinimum(tx []byte, outs []build.Output) (bool, error) {
	t, err := conway.NewConwayTransactionFromCbor(tx)
	if err != nil {
		return false, err
	}
	built := t.Outputs()
	if len(built) < len(outs) {
		return false, fmt.Errorf("fan-out has %d outputs, want %d", len(built), len(outs))
	}
	changed := false
	for i := range outs {
		size, err := common.TransactionOutputCborSize(built[i])
		if err != nil {
			return false, err
		}
		if m := (160 + uint64(size)) * e.cfg.Build.CoinsPerUtxoByte; m != outs[i].Amount {
			outs[i].Amount, changed = m, true
		}
	}
	return changed, nil
}

// grab spends a shared script output: contention, or a front-run when the
// step bids a tip.
func (e *Engine) grab(ctx context.Context, at sched.Time, id population.ActorID, iid uint64, step adversary.Step) (shot, string, error) {
	ref := Ref{Tx: TxID(step.Spend.Ref.Tx), Index: step.Spend.Ref.Index}
	s := shot{rec: wire.Pending{Args: wire.Args{Kind: unlockKind, Locked: &ref, Amount: step.Spend.Lovelace}}, ttl: e.in.TTL(at, false, true)}
	// The honest spend it races may land first.
	s.want = expect(e.cfg.Build, 0, true, intent.Again(unlockKind))
	coins, why := e.fund(id, iid, scriptHeadroom)
	if why != "" {
		return shot{}, why, nil
	}
	w := e.co.Wallet(id)
	b, err := e.in.Unlock(ctx, w, build.Spend{Inputs: e.ledgerUtxos(w, coins), TTL: s.ttl, Tip: step.Tip}, &s.rec)
	if err != nil {
		return shot{}, "", err
	}
	s.txs, s.inputs, s.cost = []build.Built{b}, refs(coins), b.Fee
	return s, "", nil
}

// malformed builds a valid self-payment and mutates it, or raw-builds a
// steal of the victim's output.
func (e *Engine) malformed(ctx context.Context, at sched.Time, id population.ActorID, iid uint64, step adversary.Step) (shot, string, error) {
	s := shot{rec: wire.Pending{Args: wire.Args{To: uint64(id), Amount: minPayment}}, ttl: e.in.TTL(at, false, false), mutation: step.Mutation.String()}
	s.want = adversary.Expect(step.Mutation)
	coins, why := e.fund(id, iid, minPayment+feeHeadroom)
	if why != "" {
		return shot{}, why, nil
	}
	w := e.co.Wallet(id)
	base, err := e.builder.Pay(ctx, w, build.Payment{Inputs: e.ledgerUtxos(w, coins), TTL: s.ttl, To: w.Address(), Amount: minPayment})
	if err != nil {
		return shot{}, "", err
	}
	tx, cost, err := e.mutant(id, w, coins, base, step, &s.rec)
	if err != nil {
		return shot{}, "", err
	}
	if tx == nil {
		return shot{}, "mutation " + step.Mutation.String() + " not applicable", nil
	}
	b := build.Built{Tx: tx, ID: mutantID(tx), Fee: cost}
	s.txs, s.inputs, s.cost = []build.Built{b}, refs(coins), cost
	return s, "", nil
}

// mutant is step's malformed transaction and its cost, nil when the
// mutation does not apply to base.
func (e *Engine) mutant(id population.ActorID, w keys.Wallet, coins []Coin, base build.Built, step adversary.Step, p *wire.Pending) ([]byte, uint64, error) {
	m := step.Mutation
	if ks, ok := m.Mistake(); ok {
		p.Actor = uint64(id)
		tx, _, err := mistake.Inject(base.Tx, ks, e.jd.Ctx(p, w))
		if err == mistake.ErrNotApplicable {
			return nil, 0, nil
		}
		return tx, base.Fee, err
	}
	if m != adversary.Steal {
		tx, err := adversary.Mutate(base.Tx, m)
		return tx, base.Fee, err
	}
	ins := make([]adversary.Coin, 0, len(coins)+1)
	for _, c := range coins {
		ins = append(ins, adversary.Coin{Ref: adversary.Ref{Tx: c.Ref.Tx, Index: c.Ref.Index}, Owner: id, Lovelace: c.Lovelace})
	}
	tx, err := adversary.RawSpend(adversary.RawSpendOf{
		Inputs: append(ins, *step.Victim), To: w.Address(), Fee: adversary.StealFee,
		TTL: base.TTL, Signers: []keys.Signer{w.PaymentSigner()},
	})
	return tx, adversary.StealFee, err
}

// mutantID is the hash a node files a mutant under: its body hash when it
// still decodes, otherwise the hash of the bytes, which only names it.
func mutantID(tx []byte) common.Blake2b256 {
	if t, err := conway.NewConwayTransactionFromCbor(tx); err == nil {
		return t.Hash()
	}
	return blake2b256(tx)
}

// race pays each payee from the same inputs.
func (e *Engine) race(ctx context.Context, at sched.Time, id population.ActorID, iid uint64, step adversary.Step) (shot, string, error) {
	s := shot{rec: wire.Pending{Args: wire.Args{To: uint64(step.Payees[0].Actor), Amount: minPayment}}, want: mistake.Expect(0, false), ttl: e.in.TTL(at, false, false)}
	coins, why := e.fund(id, iid, minPayment+feeHeadroom)
	if why != "" {
		return shot{}, why, nil
	}
	w := e.co.Wallet(id)
	for _, p := range step.Payees {
		b, err := e.builder.Pay(ctx, w, build.Payment{Inputs: e.ledgerUtxos(w, coins), TTL: s.ttl, To: e.co.Wallet(p.Actor).Address(), Amount: minPayment})
		if err != nil {
			return shot{}, "", err
		}
		s.txs = append(s.txs, b)
		s.payees = append(s.payees, uint64(p.Actor))
		// Only one can land, so the step costs its dearest.
		s.cost = max(s.cost, b.Fee+minPayment)
	}
	s.inputs = refs(coins)
	return s, "", nil
}

// gapToken is what a gap transaction mints under its burn policy.
var gapToken = []byte("gap")

// gap mints under a burn loop sized to spend the step's memory, declaring
// what the builder measured it spend, which must land between the block
// and transaction limits.
func (e *Engine) gap(ctx context.Context, at sched.Time, id population.ActorID, iid uint64, step adversary.Step) (shot, string, error) {
	s := shot{rec: wire.Pending{Args: wire.Args{To: uint64(id)}}, want: adversary.GapExpect(), ttl: e.in.TTL(at, false, false)}
	coins, why := e.fund(id, iid, 2*adversary.GapCollateral+scriptHeadroom)
	if why != "" {
		return shot{}, why, nil
	}
	w := e.co.Wallet(id)
	b, err := e.builder.Plutus().Mint(ctx, w, build.Mint{
		Spend: build.Spend{Inputs: e.ledgerUtxos(w, coins), TTL: s.ttl}, Policy: build.Burn(build.BurnSize(step.Memory)),
		Name: gapToken, Quantity: 1, Redeemer: intent.UnitDatum(), Collateral: adversary.GapCollateral,
	})
	if err != nil {
		return shot{}, "", err
	}
	u, err := build.Declared(b.Tx)
	if err != nil {
		return shot{}, "", err
	}
	if blk := e.cfg.Build.MaxBlockExUnits; u.Memory <= blk.Memory && u.Steps <= blk.Steps {
		return shot{}, fmt.Sprintf("burn declares %d memory, within the block's %d", u.Memory, blk.Memory), nil
	}
	s.txs, s.inputs, s.cost = []build.Built{b}, refs(coins), b.Fee
	return s, "", nil
}

// attackResult judges and records the node's answers to an attack. A
// first-send rejection writes the attempt off; the intent is released when
// none is left.
func (e *Engine) attackResult(ctx context.Context, at sched.Time, p *wire.Pending, r SubmitResult) error {
	out, err := pay.Outcome(r)
	if err != nil {
		return err
	}
	if err := e.judgeAttack(at, p, r, out); err != nil {
		return err
	}
	var dead []plan.TxID
	refuse := func(tx TxID) error {
		d, err := e.pay.Refused(ctx, plan.TxID(tx))
		dead = append(dead, d...)
		return err
	}
	if pay.Refusable(r.Verdict, r.Resend) {
		if err := refuse(p.Tx); err != nil {
			return fmt.Errorf("engine: refuse attack %d: %w", p.ID, err)
		}
	}
	for i, a := range r.Rivals {
		if !pay.Refusable(a.Verdict, r.Resend) || i >= len(p.Rivals) {
			continue
		}
		if err := refuse(p.Rivals[i].ID); err != nil {
			return fmt.Errorf("engine: refuse rival of attack %d: %w", p.ID, err)
		}
	}
	p.Awaiting, p.Cbor, p.Rivals = false, nil, nil
	e.pay.Unawait(p.ID)
	return e.retry.Release(ctx, at, dead, pay.Reason(r))
}

// judgeAttack holds every answer to the verdict the step called for. A
// rival may be accepted only while no copy of the race was: one accepted
// beside another is a double spend in a mempool.
func (e *Engine) judgeAttack(at sched.Time, p *wire.Pending, r SubmitResult, out submit.Outcome) error {
	node := oracle.Node{Impl: "node"}
	if r.Node != nil {
		node = *r.Node
	}
	want := p.Want.Expected()
	if p.TTL <= e.Tip().Slot+1+e.cfg.BlockEvery {
		want = withClasses(want, submit.OutsideValidityInterval)
	}
	a := oracle.Attempt{Seq: e.jr.Seq(), Tx: p.Cbor, ID: chain.TxID(p.Tx), Expected: &want, Answers: []oracle.Answer{{Node: node, Outcome: out}}}
	fs := oracle.Judge(a, e.cfg.Run)
	taken := out.Verdict == submit.Accepted
	for i, ans := range r.Rivals {
		if i >= len(p.Rivals) {
			break
		}
		rw := mistake.Expect(0, true)
		rw.Accept = !taken
		ra := oracle.Attempt{Seq: e.jr.Seq(), Tx: p.Rivals[i].Tx, ID: chain.TxID(p.Rivals[i].ID), Expected: &rw,
			Answers: []oracle.Answer{{Node: node, Outcome: answerOutcome(ans)}}}
		fs = append(fs, oracle.Judge(ra, e.cfg.Run)...)
		taken = taken || ans.Verdict == VerdictAccepted
	}
	return e.jd.Report(at, fs)
}

// Shared implements adversary.Sight: the honest locked outputs at the
// shared script address no adversary is already spending.
func (v view) Shared() []adversary.Locked {
	e := v.e
	busy, taken := map[Ref]bool{}, map[Ref]bool{}
	for q := range e.pay.All() {
		if q.Locked == nil {
			continue
		}
		if q.Attack == "" {
			busy[*q.Locked] = true
		} else {
			taken[*q.Locked] = true
		}
	}
	var out []adversary.Locked
	for _, u := range e.win.View().UtxosAt(build.AlwaysSucceeds.Address(e.cfg.NetworkID)) {
		ref := Ref{Tx: TxID(u.Id.Id()), Index: u.Id.Index()}
		owner, ok := e.lockOwner(u.Output.Datum())
		if !ok || taken[ref] {
			continue
		}
		out = append(out, adversary.Locked{Ref: adversary.Ref{Tx: ref.Tx, Index: ref.Index}, Owner: owner,
			Lovelace: u.Output.Amount().Uint64(), Busy: busy[ref]})
	}
	slices.SortFunc(out, func(a, b adversary.Locked) int { return cmpRef(a.Ref, b.Ref) })
	return out
}

func cmpRef(a, b adversary.Ref) int {
	return cmp.Or(bytes.Compare(a.Tx[:], b.Tx[:]), cmp.Compare(a.Index, b.Index))
}

// lockOwner is the honest actor whose datum d is (actorDatum).
func (e *Engine) lockOwner(d *common.Datum) (population.ActorID, bool) {
	if d == nil {
		return 0, false
	}
	if len(e.owners) != len(e.co.IDs()) {
		e.owners = make(map[string]population.ActorID, len(e.co.IDs()))
		for _, id := range e.co.IDs() {
			e.owners[string(intent.DatumKey(intent.ActorDatum(id)))] = id
		}
	}
	id, ok := e.owners[string(intent.DatumKey(*d))]
	return id, ok && !e.isAdversary(id)
}

// Payees implements adversary.Sight: the honest wallets not abandoned.
func (v view) Payees() []population.ActorID {
	var out []population.ActorID
	for _, id := range v.e.co.IDs() {
		if v.e.isAdversary(id) || v.e.abandoned(id) || v.e.co.Rec(id).Prof.Kind != profile.Wallet {
			continue
		}
		out = append(out, id)
	}
	return out
}

// Victims implements adversary.Sight: the first maxVictims settled honest
// outputs no intent holds, by Ref.
func (v view) Victims() []adversary.Coin {
	var out []adversary.Coin
	for _, u := range v.e.pay.Reads().Settled() {
		if v.e.isAdversary(u.Owner) || u.Owner == faucetActor {
			continue
		}
		if _, held := v.e.pay.Reads().Holder(u.Ref); held {
			continue
		}
		out = append(out, adversary.Coin{Ref: adversary.Ref{Tx: u.Ref.Tx, Index: u.Ref.Index}, Owner: u.Owner, Lovelace: u.Lovelace})
	}
	slices.SortFunc(out, func(a, b adversary.Coin) int { return cmpRef(a.Ref, b.Ref) })
	return out[:min(len(out), maxVictims)]
}

// Remaining implements adversary.View.
func (v view) Remaining(id population.ActorID) uint64 {
	return v.e.costs.Remaining(id, v.e.cfg.Slots.EpochAt(v.e.clk.Now()))
}

var _ adversary.View = view{}

func oneShotBuild(context.Context, keys.Wallet, build.Spend, *wire.Pending) (build.Built, error) {
	return build.Built{}, fmt.Errorf("engine: a one-shot attack is never rebuilt")
}

func isAttack(k profile.IntentKind) bool {
	_, ok := adversary.Of(k)
	return ok
}

func cmpLogRef(a, b Ref) int {
	return cmp.Or(bytes.Compare(a.Tx[:], b.Tx[:]), cmp.Compare(a.Index, b.Index))
}
