package attack

import (
	"context"
	"fmt"

	"github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/gouroboros/ledger/conway"

	"github.com/chrisguiney/cardano-continuum/internal/adversary"
	"github.com/chrisguiney/cardano-continuum/internal/build"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/wire"
	"github.com/chrisguiney/cardano-continuum/internal/keys"
	"github.com/chrisguiney/cardano-continuum/internal/mistake"
	"github.com/chrisguiney/cardano-continuum/internal/plan"
	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

// dustGuess sizes a dust output for the first build, which only measures
// the outputs' encoded size; the second build pays each its min-UTxO.
const dustGuess = 1_000_000

// gapToken is what a gap transaction mints under its burn policy.
var gapToken = []byte("gap")

// shot is one attack step built: its transactions (the first is sent
// first, the rest are rivals on the same inputs), what they spend and
// cost, and the record the pending intent starts from.
type shot struct {
	txs      []build.Built
	inputs   []wire.Ref
	ttl      uint64
	cost     uint64
	want     mistake.Expected
	rec      wire.Pending
	payees   []uint64
	mutation string
}

// maker builds each attack's step.
type maker struct{ core *core }

// build makes step's transactions for adversary id.
func (m *maker) build(ctx context.Context, at sched.Time, id population.ActorID, iid uint64, step adversary.Step) (shot, string, error) {
	var s shot
	var why string
	var err error
	switch step.Attack {
	case adversary.DustFlood:
		s, why, err = m.dust(ctx, at, id, iid, step)
	case adversary.Contention, adversary.FrontRun:
		s, why, err = m.grab(ctx, at, id, iid, step)
	case adversary.Malformed:
		s, why, err = m.malformed(ctx, at, id, iid, step)
	case adversary.DoubleSpend:
		s, why, err = m.race(ctx, at, id, iid, step)
	case adversary.ExUnitsGap:
		s, why, err = m.gap(ctx, at, id, iid, step)
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
func (m *maker) fund(id population.ActorID, iid uint64, need uint64) ([]wire.Coin, string) {
	avail := m.core.env.Ledger.Coins(id, plan.IntentID(iid))
	coins := m.core.spec.Selector.Select(avail, need)
	if coins == nil {
		return nil, fmt.Sprintf("spendable %d below %d", wire.SumCoins(avail), need)
	}
	return coins, ""
}

func refs(coins []wire.Coin) []wire.Ref {
	out := make([]wire.Ref, len(coins))
	for i, c := range coins {
		out[i] = c.Ref
	}
	return out
}

// dust pays every payee one output of exactly its min-UTxO.
func (m *maker) dust(ctx context.Context, at sched.Time, id population.ActorID, iid uint64, step adversary.Step) (shot, string, error) {
	sp, env := &m.core.spec, &m.core.env
	outs := make([]build.Output, len(step.Payees))
	s := shot{rec: wire.Pending{Args: wire.Args{Kind: DustKind}}, want: mistake.Expect(0, false), ttl: env.Maker.TTL(at, false, false)}
	for i, p := range step.Payees {
		outs[i] = build.Output{To: build.AlwaysSucceeds.Address(sp.NetworkID), Amount: dustGuess}
		if !p.Script {
			outs[i].To = env.Roster.Wallet(p.Actor).Address()
		}
		s.payees = append(s.payees, uint64(p.Actor))
	}
	coins, why := m.fund(id, iid, uint64(len(outs))*dustGuess*2+sp.FeeHeadroom)
	if why != "" {
		return shot{}, why, nil
	}
	w := env.Roster.Wallet(id)
	spend := build.Spend{Inputs: env.Spend.Utxos(w, coins), TTL: s.ttl}
	b, err := sp.Builder.FanOut(ctx, w, spend, outs)
	// A min-UTxO depends on the output's encoded size, which depends on
	// its coin: rebuild until the amounts are the minimum of themselves.
	for pass := 0; err == nil && pass < 3; pass++ {
		var changed bool
		if changed, err = m.atMinimum(b.Tx, outs); err != nil || !changed {
			break
		}
		b, err = sp.Builder.FanOut(ctx, w, spend, outs)
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
func (m *maker) atMinimum(tx []byte, outs []build.Output) (bool, error) {
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
		if least := (160 + uint64(size)) * m.core.spec.Build.CoinsPerUtxoByte; least != outs[i].Amount {
			outs[i].Amount, changed = least, true
		}
	}
	return changed, nil
}

// grab spends a shared script output: contention, or a front-run when the
// step bids a tip.
func (m *maker) grab(ctx context.Context, at sched.Time, id population.ActorID, iid uint64, step adversary.Step) (shot, string, error) {
	sp, env := &m.core.spec, &m.core.env
	ref := wire.Ref{Tx: wire.TxID(step.Spend.Ref.Tx), Index: step.Spend.Ref.Index}
	s := shot{rec: wire.Pending{Args: wire.Args{Kind: sp.Unlock, Locked: &ref, Amount: step.Spend.Lovelace}}, ttl: env.Maker.TTL(at, false, true)}
	// The honest spend it races may land first.
	s.want = env.Judge.Expect(0, true, sp.Again)
	coins, why := m.fund(id, iid, sp.ScriptHeadroom)
	if why != "" {
		return shot{}, why, nil
	}
	w := env.Roster.Wallet(id)
	b, err := env.Maker.Unlock(ctx, w, build.Spend{Inputs: env.Spend.Utxos(w, coins), TTL: s.ttl, Tip: step.Tip}, &s.rec)
	if err != nil {
		return shot{}, "", err
	}
	s.txs, s.inputs, s.cost = []build.Built{b}, refs(coins), b.Fee
	return s, "", nil
}

// malformed builds a valid self-payment and mutates it, or raw-builds a
// steal of the victim's output.
func (m *maker) malformed(ctx context.Context, at sched.Time, id population.ActorID, iid uint64, step adversary.Step) (shot, string, error) {
	sp, env := &m.core.spec, &m.core.env
	s := shot{rec: wire.Pending{Args: wire.Args{To: uint64(id), Amount: sp.MinPayment}}, ttl: env.Maker.TTL(at, false, false), mutation: step.Mutation.String()}
	s.want = adversary.Expect(step.Mutation)
	coins, why := m.fund(id, iid, sp.MinPayment+sp.FeeHeadroom)
	if why != "" {
		return shot{}, why, nil
	}
	w := env.Roster.Wallet(id)
	base, err := sp.Builder.Pay(ctx, w, build.Payment{Inputs: env.Spend.Utxos(w, coins), TTL: s.ttl, To: w.Address(), Amount: sp.MinPayment})
	if err != nil {
		return shot{}, "", err
	}
	tx, cost, err := m.mutant(id, w, coins, base, step, &s.rec)
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
func (m *maker) mutant(id population.ActorID, w keys.Wallet, coins []wire.Coin, base build.Built, step adversary.Step, p *wire.Pending) ([]byte, uint64, error) {
	mu := step.Mutation
	if ks, ok := mu.Mistake(); ok {
		p.Actor = uint64(id)
		tx, _, err := mistake.Inject(base.Tx, ks, m.core.env.Judge.Ctx(p, w))
		if err == mistake.ErrNotApplicable {
			return nil, 0, nil
		}
		return tx, base.Fee, err
	}
	if mu != adversary.Steal {
		tx, err := adversary.Mutate(base.Tx, mu)
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
	return common.Blake2b256Hash(tx)
}

// race pays each payee from the same inputs.
func (m *maker) race(ctx context.Context, at sched.Time, id population.ActorID, iid uint64, step adversary.Step) (shot, string, error) {
	sp, env := &m.core.spec, &m.core.env
	s := shot{rec: wire.Pending{Args: wire.Args{To: uint64(step.Payees[0].Actor), Amount: sp.MinPayment}}, want: mistake.Expect(0, false), ttl: env.Maker.TTL(at, false, false)}
	coins, why := m.fund(id, iid, sp.MinPayment+sp.FeeHeadroom)
	if why != "" {
		return shot{}, why, nil
	}
	w := env.Roster.Wallet(id)
	for _, p := range step.Payees {
		b, err := sp.Builder.Pay(ctx, w, build.Payment{Inputs: env.Spend.Utxos(w, coins), TTL: s.ttl, To: env.Roster.Wallet(p.Actor).Address(), Amount: sp.MinPayment})
		if err != nil {
			return shot{}, "", err
		}
		s.txs = append(s.txs, b)
		s.payees = append(s.payees, uint64(p.Actor))
		// Only one can land, so the step costs its dearest.
		s.cost = max(s.cost, b.Fee+sp.MinPayment)
	}
	s.inputs = refs(coins)
	return s, "", nil
}

// gap mints under a burn loop sized to spend the step's memory, declaring
// what the builder measured it spend, which must land between the block
// and transaction limits.
func (m *maker) gap(ctx context.Context, at sched.Time, id population.ActorID, iid uint64, step adversary.Step) (shot, string, error) {
	sp, env := &m.core.spec, &m.core.env
	s := shot{rec: wire.Pending{Args: wire.Args{To: uint64(id)}}, want: adversary.GapExpect(), ttl: env.Maker.TTL(at, false, false)}
	coins, why := m.fund(id, iid, 2*adversary.GapCollateral+sp.ScriptHeadroom)
	if why != "" {
		return shot{}, why, nil
	}
	w := env.Roster.Wallet(id)
	b, err := sp.Builder.Plutus().Mint(ctx, w, build.Mint{
		Spend: build.Spend{Inputs: env.Spend.Utxos(w, coins), TTL: s.ttl}, Policy: build.Burn(build.BurnSize(step.Memory)),
		Name: gapToken, Quantity: 1, Redeemer: sp.Unit, Collateral: adversary.GapCollateral,
	})
	if err != nil {
		return shot{}, "", err
	}
	u, err := build.Declared(b.Tx)
	if err != nil {
		return shot{}, "", err
	}
	if blk := sp.Build.MaxBlockExUnits; u.Memory <= blk.Memory && u.Steps <= blk.Steps {
		return shot{}, fmt.Sprintf("burn declares %d memory, within the block's %d", u.Memory, blk.Memory), nil
	}
	s.txs, s.inputs, s.cost = []build.Built{b}, refs(coins), b.Fee
	return s, "", nil
}
