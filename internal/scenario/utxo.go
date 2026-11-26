package scenario

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/blinklabs-io/gouroboros/ledger/conway"

	"github.com/chrisguiney/cardano-continuum/internal/keys"
	"github.com/chrisguiney/cardano-continuum/internal/mistake"
	"github.com/chrisguiney/cardano-continuum/internal/submit"
	"github.com/chrisguiney/cardano-continuum/internal/tla"
)

// utxoMapping holds the choices of the ledger-utxo refinement mapping that a
// test replaces to show that a wrong mapping is caught as a disagreement.
type utxoMapping struct {
	// upper is the concrete invalid_hereafter for the model's upper bound
	// hi, where off is the concrete slot of the model's slot 0.
	upper func(era string, off uint64, hi int) uint64
}

// upperBound keeps the last slot a transaction is valid in. From Allegra the
// model's bound is already exclusive, as Conway's invalid_hereafter is.
// Shelley's TTL is inclusive, so it becomes the slot after it.
func upperBound(era string, off uint64, hi int) uint64 {
	if era == "Shelley" {
		return off + uint64(hi) + 1
	}
	return off + uint64(hi)
}

// UtxoAdapter is the refinement mapping of Utxo.tla (see the package
// documentation for the table).
type UtxoAdapter struct{ m utxoMapping }

// NewUtxoAdapter returns the ledger-utxo adapter.
func NewUtxoAdapter() *UtxoAdapter { return &UtxoAdapter{m: utxoMapping{upper: upperBound}} }

// Family implements Adapter.
func (*UtxoAdapter) Family() string { return FamilyUtxo }

// utxoClasses are the failures a rejected Utxo.tla transaction may be given:
// one per conjunct of SpecAccepts, plus Conway's own disjointness rule.
var utxoClasses = []submit.Class{
	submit.BadInputs, submit.FeeTooSmall, submit.ValueNotConserved,
	submit.OutsideValidityInterval, submit.OutputTooSmall, submit.OutputTooBig,
	submit.InputSetEmpty, submit.InsufficientCollateral, submit.ValidationTagMismatch,
	submit.NonDisjointRefInputs,
	"NoCollateralInputs", "CollateralContainsNonADA",
	"StakeKeyRegisteredDELEG", "StakeKeyNotRegisteredDELEG", "WithdrawalsNotInRewardsCERTS",
}

// Expected implements Adapter: SpecAccepts is the whole verdict.
func (*UtxoAdapter) Expected(s Scenario, i int) mistake.Expected {
	if s.Utxo.Steps[i].Accepts {
		return mistake.Expected{Accept: true}
	}
	return mistake.Expected{Classes: sorted(utxoClasses)}
}

// Applicable implements Adapter.
func (*UtxoAdapter) Applicable(s Scenario, _ Env) error {
	c := s.Utxo
	if c == nil {
		return unmappable(-1, "scenario has no ledger-utxo case")
	}
	if !slices.Contains(tla.UtxoEras, c.Era) {
		return unmappable(-1, "era %q is not one Utxo.tla models", c.Era)
	}
	if len(c.Steps) == 0 {
		return unmappable(-1, "no transaction to offer")
	}
	if strings.HasPrefix(c.Mutation, "Spec") {
		return unmappable(-1, "the configuration mutates the reference itself, so its verdicts are not the reference's")
	}
	return nil
}

// ledger is Utxo.tla's state.
type ledger struct {
	utxo       map[string]tla.Output
	slot       int
	registered bool
	reward     int
}

func ledgerFrom(l tla.Ledger) ledger {
	return ledger{utxo: l.UTxO, slot: l.Slot, registered: l.Registered, reward: l.Reward}
}

// effect is what a transaction moves, in model units (Utxo.tla Consumed,
// Deposit, Refund, Wdrl, OutCoin, OutTok).
type effect struct {
	wdrl, refund, deposit int
	// base is OutCoin without delta: what a balanced output would hold.
	base   int
	outTok int
}

func (l ledger) sum(ids []string, f func(tla.Output) int) int {
	n := 0
	for _, id := range ids {
		if o, ok := l.utxo[id]; ok {
			n += f(o)
		}
	}
	return n
}

func (l ledger) eval(tx tla.AbstractTx) effect {
	var e effect
	if tx.Wdrl {
		e.wdrl = l.reward
	}
	if tx.Cert == "dereg" && l.registered {
		e.refund = 1
	}
	if tx.Cert == "reg" {
		e.deposit = 1
	}
	coins := l.sum(tx.Ins, func(o tla.Output) int { return o.Coin })
	e.base = coins + e.wdrl + e.refund - tx.Fee - e.deposit
	e.outTok = l.sum(tx.Ins, func(o tla.Output) int { return o.Tok }) + tx.Mint + tx.TDelta
	return e
}

// apply moves the ledger to the model's own next state and returns the place
// the transaction's output landed in. The model picks that place with CHOOSE,
// so it is read from the next state rather than recomputed; the output found
// there must be the one the mapping's arithmetic expects.
func (l ledger) apply(v tla.Verdict, e effect) (ledger, string, error) {
	if v.After == nil {
		return ledger{}, "", fmt.Errorf("accepted transaction has no next ledger")
	}
	want := tla.Output{Coin: e.base + v.Tx.Delta, Tok: e.outTok, Lock: v.Tx.OLock}
	var place []string
	for id, o := range v.After.UTxO {
		_, was := l.utxo[id]
		if !was || slices.Contains(v.Tx.Ins, id) {
			place = append(place, id)
			if o != want {
				return ledger{}, "", fmt.Errorf("model output %+v at %s, mapping computed %+v", o, id, want)
			}
			continue
		}
		if o != l.utxo[id] {
			return ledger{}, "", fmt.Errorf("place %s changed without being spent", id)
		}
	}
	if len(place) != 1 {
		return ledger{}, "", fmt.Errorf("transaction made %d outputs, want 1", len(place))
	}
	return ledgerFrom(*v.After), place[0], nil
}

// blocked says why the mapping cannot carry one step to a Conway target, or
// "". Each reason is a cell where the model's era or the target differs from
// what the verdict assumes.
func blocked(era string, l ledger, v tla.Verdict, e effect, caps Caps) string {
	tx := v.Tx
	overlap := slices.ContainsFunc(tx.Refs, func(id string) bool { return slices.Contains(tx.Ins, id) })
	switch {
	case !tx.Valid && v.Accepts:
		return "applying a phase-2-invalid transaction (collateral taken) is not concretized"
	case !tx.Valid && !caps.Phase2Invalid:
		return "target does not model phase-2-invalid transactions"
	case tx.Rdmrs:
		return "redeemers need a Plutus script, which this adapter does not attach"
	case tx.Big:
		return "an output over the maximum value size is not concretized"
	case e.base < 0 || e.base == 0 && tx.Delta < 0:
		return "output would hold a negative amount"
	case era == "Mary" && v.Accepts && e.base+tx.Delta < 1 && e.outTok > 0:
		return "Mary accepts a token output below the coin minimum; Conway does not"
	case v.Accepts && overlap:
		return "Babbage lets a spent input also be a reference input; Conway before protocol version 11 does not"
	case l.reward > 0 && (tx.Wdrl || tx.Cert == "dereg") && !caps.Rewards:
		return "target cannot give the reward account a balance"
	}
	return ""
}

// mapped is one abstract step the mapping carries, with the ledger it is
// offered to.
type mapped struct {
	index  int
	before ledger
	v      tla.Verdict
	e      effect
	place  string // where an accepted step's output lands
}

// utxoPrep is a scenario read through the mapping, before any byte is made.
type utxoPrep struct {
	w      world
	c      *UtxoCase
	unit   uint64
	params conway.ConwayProtocolParameters
	places []string // the start ledger's live places, sorted
	steps  []mapped
	plan   Plan
}

func (a *UtxoAdapter) prepare(s Scenario, env Env) (utxoPrep, error) {
	if err := a.Applicable(s, env); err != nil {
		return utxoPrep{}, err
	}
	w, err := newWorld(env)
	if err != nil {
		return utxoPrep{}, err
	}
	p := utxoPrep{w: w, c: s.Utxo}
	l := ledgerFrom(p.c.Start)
	for id := range l.utxo {
		p.places = append(p.places, id)
	}
	sort.Strings(p.places)
	for i, v := range p.c.Steps {
		e := l.eval(v.Tx)
		if why := blocked(p.c.Era, l, v, e, env.Caps); why != "" {
			if v.Accepts {
				p.plan.Truncated = &Skip{Index: i, Reason: why}
				break
			}
			p.plan.Skipped = append(p.plan.Skipped, Skip{Index: i, Reason: why})
			continue
		}
		m := mapped{index: i, before: l, v: v, e: e}
		// The last step's next ledger is not needed; any earlier accepted
		// step's is, and apply reports it missing.
		if v.Accepts && (v.After != nil || i < len(p.c.Steps)-1) {
			if l, m.place, err = l.apply(v, e); err != nil {
				return utxoPrep{}, fmt.Errorf("%s step %d: %w", s.ID, i, err)
			}
		}
		p.steps = append(p.steps, m)
	}
	if p.unit, err = unitFor(p.shapes(), &env.Params); err != nil {
		return utxoPrep{}, fmt.Errorf("%s: %w", s.ID, err)
	}
	p.params = pinParams(env.Params, p.unit)
	return p, nil
}

// shapes lists every output the scenario writes, without amounts.
func (p utxoPrep) shapes() []txOut {
	var out []txOut
	for _, id := range p.places {
		o := p.c.Start.UTxO[id]
		out = append(out, txOut{Addr: p.w.lockAddr(o.Lock), Token: p.w.token(o.Tok)})
	}
	for _, m := range p.steps {
		out = append(out, txOut{Addr: p.w.lockAddr(m.v.Tx.OLock), Token: p.w.token(m.e.outTok)})
		if r := m.v.Tx.Ret; r != nil {
			out = append(out, txOut{Addr: p.w.lockAddr("key"), Token: p.w.token(r.Tok)})
		}
	}
	return append(out, txOut{Addr: p.w.lockAddr("key")})
}

// Conway certificate tags (conway.cddl).
const (
	certUnreg        = 8
	certVoteRegDeleg = 12
)

func (p utxoPrep) stakeCred() []byte {
	h := p.w.scratch.StakeKeyHash()
	return cArray(cUint(0), cBytes(h[:]))
}

// regCert registers the stake credential and delegates its vote to "always
// abstain" in one certificate. The model's registration carries no vote, but
// from protocol version 10 Conway refuses a withdrawal from an account with
// no DRep, and the model lets a registered credential withdraw.
func (p utxoPrep) regCert() []byte {
	return cArray(cUint(certVoteRegDeleg), p.stakeCred(), cArray(cUint(2)), cUint(p.unit))
}

func (p utxoPrep) unregCert() []byte {
	return cArray(cUint(certUnreg), p.stakeCred(), cUint(p.unit))
}

// funds is the lovelace the setup transaction spends: every place, its fee
// and the starting registration's deposit.
func (p utxoPrep) funds() uint64 {
	total := p.unit
	for _, id := range p.places {
		total += uint64(p.c.Start.UTxO[id].Coin) * p.unit
	}
	if p.c.Start.Registered {
		total += p.unit
	}
	return total
}

// setup builds the start ledger: one output per live place, the tokens
// minted, the credential registered.
func (p utxoPrep) setup() txSpec {
	t := txSpec{Inputs: []txIn{p.w.genesisRef()}, Fee: p.unit, Signers: []keys.Signer{p.w.funder.PaymentSigner()}}
	toks := 0
	for _, id := range p.places {
		o := p.c.Start.UTxO[id]
		toks += o.Tok
		t.Outputs = append(t.Outputs, txOut{Addr: p.w.lockAddr(o.Lock), Coin: uint64(o.Coin) * p.unit, Token: p.w.token(o.Tok)})
	}
	if toks > 0 {
		t.Mint, t.Natives = p.w.token(toks), [][]byte{anyoneScript}
	}
	if p.c.Start.Registered {
		t.Certs = [][]byte{p.regCert()}
		t.Signers = append(t.Signers, p.w.scratch.StakeSigner())
	}
	return t
}

// Preconditions implements Adapter.
func (a *UtxoAdapter) Preconditions(s Scenario, env Env) (Preconditions, error) {
	p, err := a.prepare(s, env)
	if err != nil {
		return Preconditions{}, err
	}
	return Preconditions{
		Era: targetEra, ProtocolMajor: targetMajor, Params: p.params,
		Funder: p.w.funder.Address(), Funds: p.funds(), UTxOs: len(p.places),
	}, nil
}

// missingIndex is the output index a place that never held an output is
// named by: an index the setup transaction does not have.
const missingIndex = 100

// Concretize implements Adapter. The setup transaction is validated at
// env.Start and confirmed by the block of the next slot, so every step is
// validated at env.Start+2, the slot the model's current slot stands for.
func (a *UtxoAdapter) Concretize(s Scenario, env Env) (Plan, error) {
	p, err := a.prepare(s, env)
	if err != nil {
		return Plan{}, err
	}
	plan := p.plan
	if len(p.steps) == 0 {
		return plan, nil
	}
	setup, setupID := p.setup().encode()
	refs := map[string]txIn{}
	for i, id := range []string{"a", "b", "c"} {
		refs[id] = txIn{ID: setupID, Index: missingIndex + uint32(i)}
	}
	for i, id := range p.places {
		refs[id] = txIn{ID: setupID, Index: uint32(i)}
	}
	now := env.Start + 2
	plan.Steps = []Step{
		{Kind: Submit, Note: "build the start ledger", Index: -1, Tx: setup, ID: setupID, Setup: true, Slot: env.Start, Expect: mistake.Expected{Accept: true}},
		{Kind: Wait, Note: "confirm the start ledger", Index: -1, Slot: env.Start + 1},
	}
	for _, m := range p.steps {
		tx, id := a.txFor(p, m, refs, now).encode()
		plan.Steps = append(plan.Steps, Step{
			Kind: Submit, Note: fmt.Sprintf("offer transaction %d", m.index), Index: m.index,
			Tx: tx, ID: id, Slot: now, Expect: a.Expected(s, m.index),
		})
		if m.place != "" {
			refs[m.place] = txIn{ID: id}
		}
	}
	return plan, nil
}

func pick(refs map[string]txIn, ids []string) []txIn {
	out := make([]txIn, len(ids))
	for i, id := range ids {
		out[i] = refs[id]
	}
	return out
}

// txFor writes one abstract transaction as a Conway transaction validated at
// slot now.
func (a *UtxoAdapter) txFor(p utxoPrep, m mapped, refs map[string]txIn, now uint64) txSpec {
	tx, u := m.v.Tx, p.unit
	t := txSpec{
		Inputs: pick(refs, tx.Ins), Collateral: pick(refs, tx.Coll), Refs: pick(refs, tx.Refs),
		Fee: uint64(tx.Fee) * u, Mint: mintOf(p.w, tx.Mint),
	}
	// The one-coin imbalance of delta is one lovelace: the smallest amount
	// by which value can fail to be conserved.
	coin := uint64(int64(m.e.base)*int64(u) + int64(tx.Delta))
	t.Outputs = []txOut{{Addr: p.w.lockAddr(tx.OLock), Coin: coin, Token: p.w.token(m.e.outTok)}}
	off := now - uint64(m.before.slot)
	if tx.Lo != 0 {
		lo := off + uint64(tx.Lo)
		t.Start = &lo
	}
	if p.c.Era == "Shelley" || tx.Hi != 0 {
		hi := a.m.upper(p.c.Era, off, tx.Hi)
		t.TTL = &hi
	}
	switch tx.Cert {
	case "reg":
		t.Certs = [][]byte{p.regCert()}
	case "dereg":
		t.Certs = [][]byte{p.unregCert()}
	}
	if tx.Wdrl {
		t.Wdrl, t.WdrlAmount = p.w.rewardAddr(), uint64(m.e.wdrl)*u
	}
	if r := tx.Ret; r != nil {
		t.Return = &txOut{Addr: p.w.lockAddr("key"), Coin: uint64(r.Coin) * u, Token: p.w.token(r.Tok)}
	}
	if tx.TColl >= 0 {
		tc := uint64(tx.TColl) * u
		t.TotalColl = &tc
	}
	spendsScript := slices.ContainsFunc(tx.Ins, func(id string) bool { return m.before.utxo[id].Lock == "script" })
	if tx.Mint != 0 || spendsScript {
		t.Natives = [][]byte{anyoneScript}
	}
	t.Signers = p.w.signers(tx.Cert != "none" || tx.Wdrl)
	return t
}

// mintOf is the mint field: n of the model's token, negative to burn.
func mintOf(w world, n int) *asset {
	if n == 0 {
		return nil
	}
	return &asset{Policy: w.policy, Name: tokenName, Qty: int64(n)}
}
