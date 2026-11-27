package engine

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"math/big"
	"math/rand/v2"
	"slices"
	"strconv"
	"strings"

	"github.com/blinklabs-io/gouroboros/cbor"
	"github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/plutigo/data"

	"github.com/chrisguiney/cardano-continuum/internal/agenda"
	"github.com/chrisguiney/cardano-continuum/internal/build"
	"github.com/chrisguiney/cardano-continuum/internal/chain"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/cohort"
	"github.com/chrisguiney/cardano-continuum/internal/fakechain"
	"github.com/chrisguiney/cardano-continuum/internal/keys"
	"github.com/chrisguiney/cardano-continuum/internal/plan"
	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
	"github.com/chrisguiney/cardano-continuum/internal/submit"
)

// Every agenda intent with a transaction goes the same way: prepare fills
// in what the intent names only by kind (payee, pool, proposal, locked
// output), the planner reserves the inputs, the builder for the kind signs
// the attempt, and the retry machine drives it. A spec is a kind's builder
// and what its inputs must cover.

// Intent kinds that are not agenda kinds: script_call is a lock or an
// unlock, decided by whether the actor has something locked.
const (
	lockKind   = "lock"
	unlockKind = "unlock"
)

const (
	// scriptHeadroom covers a script transaction's fee and collateral, which
	// must come from one pure-ada input.
	scriptHeadroom = 6_000_000
	lockAmount     = 5_000_000
	mintQuantity   = 1000
)

// tokenName is what every mint names under build.FreeMint.
var tokenName = []byte("continuum")

// scriptActor owns the always-succeeds address in the view's book, so the
// view tracks locked outputs. It is above maxActors and below the faucet.
const scriptActor = cohort.Script

type spec struct {
	// need is the lovelace the inputs cover besides headroom.
	need     func(e *Engine, p *pendingRec) uint64
	headroom uint64
	build    func(ctx context.Context, e *Engine, w keys.Wallet, sp build.Spend, p *pendingRec) (build.Built, error)
	// deposit is the lovelace a settled attempt moves into the ledger's
	// deposits (negative: refunded), for the supply check.
	deposit func(e *Engine, p *pendingRec) int64
	// guarded kinds can lower forging stake: their TTL ends inside the
	// current epoch (stake-floor rule 10).
	guarded bool
	// script kinds run a Plutus script, so their TTL stays inside the
	// stability window (Config.Horizon).
	script bool
	// again are the classes the kind's certificates fail with when the
	// same transaction already landed: what the oracle expects beside
	// "inputs spent" from a resend.
	again []submit.Class
}

var (
	againRegister   = []submit.Class{submit.Class(fakechain.StakeKeyRegistered)}
	againDeregister = []submit.Class{submit.Class(fakechain.StakeKeyNotRegistered), submit.Class(fakechain.RefundIncorrect)}
)

func none(*Engine, *pendingRec) uint64 { return 0 }

func noDeposit(*Engine, *pendingRec) int64 { return 0 }

var specs = map[string]spec{
	string(profile.IntentSend): {need: func(_ *Engine, p *pendingRec) uint64 { return p.Amount }, headroom: feeHeadroom, deposit: noDeposit,
		build: func(ctx context.Context, e *Engine, w keys.Wallet, sp build.Spend, p *pendingRec) (build.Built, error) {
			return e.builder.Pay(ctx, w, build.Payment{Inputs: sp.Inputs, TTL: sp.TTL, To: e.co.Wallet(population.ActorID(p.To)).Address(), Amount: p.Amount})
		}},
	string(profile.IntentRegisterStake): {need: func(e *Engine, _ *pendingRec) uint64 { return e.cfg.Build.KeyDeposit }, headroom: feeHeadroom, again: againRegister,
		deposit: func(e *Engine, _ *pendingRec) int64 { return int64(e.cfg.Build.KeyDeposit) },
		build: func(ctx context.Context, e *Engine, w keys.Wallet, sp build.Spend, _ *pendingRec) (build.Built, error) {
			return e.builder.Stake().RegisterStake(ctx, w, sp)
		}},
	string(profile.IntentDelegate): {need: registerNeed, headroom: feeHeadroom, guarded: true, deposit: registerDeposit, again: againRegister,
		build: func(ctx context.Context, e *Engine, w keys.Wallet, sp build.Spend, p *pendingRec) (build.Built, error) {
			pool, _ := e.co.Ring.PoolOf(population.ActorID(p.To))
			return e.builder.Stake().Delegate(ctx, w, build.Delegation{Spend: sp, Pool: pool, Register: p.Register})
		}},
	string(profile.IntentUndelegate): {need: none, headroom: feeHeadroom, guarded: true, again: againDeregister,
		deposit: func(e *Engine, _ *pendingRec) int64 { return -int64(e.cfg.Build.KeyDeposit) },
		build: func(ctx context.Context, e *Engine, w keys.Wallet, sp build.Spend, _ *pendingRec) (build.Built, error) {
			return e.builder.Stake().DeregisterStake(ctx, w, sp)
		}},
	string(profile.IntentWithdraw): {need: none, headroom: feeHeadroom, deposit: noDeposit,
		again: []submit.Class{submit.Class(fakechain.WithdrawalsNotInRewards)},
		build: func(ctx context.Context, e *Engine, w keys.Wallet, sp build.Spend, p *pendingRec) (build.Built, error) {
			return e.builder.Stake().Withdraw(ctx, w, build.Withdrawal{Spend: sp, Amount: p.Amount})
		}},
	string(profile.IntentDelegateDRep): {need: none, headroom: feeHeadroom, deposit: noDeposit,
		build: func(ctx context.Context, e *Engine, w keys.Wallet, sp build.Spend, p *pendingRec) (build.Built, error) {
			cred := e.co.Ring.DRep(population.ActorID(p.To))
			d := common.Drep{Type: common.DrepTypeAddrKeyHash, Credential: cred[:]}
			return e.builder.Stake().DelegateVote(ctx, w, build.VoteDelegation{Spend: sp, DRep: d})
		}},
	string(profile.IntentRegisterDRep): {need: func(e *Engine, _ *pendingRec) uint64 { return e.cfg.Build.DRepDeposit }, headroom: feeHeadroom,
		again:   []submit.Class{submit.Class(fakechain.DRepAlreadyRegistered)},
		deposit: func(e *Engine, _ *pendingRec) int64 { return int64(e.cfg.Build.DRepDeposit) },
		build: func(ctx context.Context, e *Engine, w keys.Wallet, sp build.Spend, p *pendingRec) (build.Built, error) {
			return e.builder.Gov().RegisterDRep(ctx, w, build.DRepRegistration{Spend: sp, DRep: e.co.Ring.DRepSigner(population.ActorID(p.Actor))})
		}},
	string(profile.IntentVote): {need: none, headroom: feeHeadroom, deposit: noDeposit, build: buildVote},
	string(profile.IntentPropose): {need: func(e *Engine, _ *pendingRec) uint64 { return e.cfg.Build.GovActionDeposit }, headroom: feeHeadroom,
		deposit: func(e *Engine, _ *pendingRec) int64 { return int64(e.cfg.Build.GovActionDeposit) },
		build: func(ctx context.Context, e *Engine, w keys.Wallet, sp build.Spend, p *pendingRec) (build.Built, error) {
			return e.builder.Gov().Propose(ctx, w, build.Proposal{Spend: sp, Action: build.ActionType(p.GovType)})
		}},
	string(profile.IntentRetireDRep): {need: none, headroom: feeHeadroom,
		again:   []submit.Class{submit.Class(fakechain.DRepNotRegistered)},
		deposit: func(e *Engine, _ *pendingRec) int64 { return -int64(e.cfg.Build.DRepDeposit) },
		build: func(ctx context.Context, e *Engine, w keys.Wallet, sp build.Spend, p *pendingRec) (build.Built, error) {
			return e.builder.Gov().RetireDRep(ctx, w, build.DRepRetirement{Spend: sp, DRep: e.co.Ring.DRepSigner(population.ActorID(p.Actor))})
		}},
	string(profile.IntentUpdatePool): {need: none, headroom: feeHeadroom, deposit: noDeposit, build: buildPool},
	string(profile.IntentMint):       {need: none, headroom: scriptHeadroom, deposit: noDeposit, build: buildMint, script: true},
	string(profile.IntentBurn):       {need: none, headroom: scriptHeadroom, deposit: noDeposit, build: buildMint, script: true},
	lockKind: {need: func(_ *Engine, p *pendingRec) uint64 { return p.Amount }, headroom: feeHeadroom,
		deposit: func(_ *Engine, p *pendingRec) int64 { return 0 },
		build: func(ctx context.Context, e *Engine, w keys.Wallet, sp build.Spend, p *pendingRec) (build.Built, error) {
			return e.builder.Plutus().Lock(ctx, w, build.Lock{Spend: sp, Script: build.Contract(p.Contract), Amount: p.Amount, Datum: actorDatum(population.ActorID(p.Actor))})
		}},
	hotKind: {need: none, headroom: feeHeadroom, deposit: noDeposit, build: buildHot},
	// A dust flood is one-shot: nothing rebuilds it.
	dustKind: {need: none, headroom: feeHeadroom, deposit: noDeposit, build: oneShotBuild},
	// A landed unlock's script input is gone, so its redeemer and script
	// witness point at nothing.
	unlockKind: {need: none, headroom: scriptHeadroom, deposit: noDeposit, build: buildUnlock, script: true,
		again: []submit.Class{submit.Class(fakechain.ExtraRedeemers), submit.MissingScriptWitnesses}},
	string(profile.IntentRegisterPool): {need: poolNeed, headroom: feeHeadroom, deposit: poolDeposit, build: buildPool},
	string(profile.IntentRetirePool): {need: none, headroom: feeHeadroom, guarded: true, deposit: noDeposit,
		build: func(ctx context.Context, e *Engine, w keys.Wallet, sp build.Spend, p *pendingRec) (build.Built, error) {
			cold, err := e.co.Ring.PoolSigner(population.ActorID(p.Actor))
			if err != nil {
				return build.Built{}, err
			}
			return e.builder.Pool().RetirePool(ctx, w, build.PoolRetirement{Spend: sp, Cold: cold, Epoch: p.Epoch, CurrentEpoch: p.Epoch - 1})
		}},
}

func registerNeed(e *Engine, p *pendingRec) uint64 {
	if p.Register {
		return e.cfg.Build.KeyDeposit
	}
	return 0
}

func registerDeposit(e *Engine, p *pendingRec) int64 { return int64(registerNeed(e, p)) }

func poolNeed(e *Engine, p *pendingRec) uint64 {
	if p.Update {
		return 0
	}
	return e.cfg.Build.PoolDeposit
}

func poolDeposit(e *Engine, p *pendingRec) int64 { return int64(poolNeed(e, p)) }

func buildVote(ctx context.Context, e *Engine, w keys.Wallet, sp build.Spend, p *pendingRec) (build.Built, error) {
	act := common.GovActionId{TransactionId: common.Blake2b256(p.Action.Tx), GovActionIdx: p.Action.Index}
	id := population.ActorID(p.Actor)
	v := build.Vote{Spend: sp, Voter: e.co.Ring.DRepSigner(id), Role: build.RoleDRep, Action: act, Choice: p.Choice}
	if _, ok := e.co.Ring.PoolOf(id); ok {
		cold, err := e.co.Ring.PoolSigner(id)
		if err != nil {
			return build.Built{}, err
		}
		v.Voter, v.Role = cold, build.RolePool
	}
	return e.builder.Gov().Vote(ctx, w, v)
}

// Voter classes as Submitted names them.
const (
	voterDRep = "drep"
	voterSPO  = "spo"
)

// voterOf is the voter an actor votes as: its pool, as an SPO, when it
// operates one, else its DRep key.
func (e *Engine) voterOf(id population.ActorID) (chain.Voter, string) {
	if pool, ok := e.co.Ring.PoolOf(id); ok {
		return chain.Voter{Kind: common.VoterTypeStakingPoolKeyHash, Hash: pool}, voterSPO
	}
	return chain.Voter{Kind: common.VoterTypeDRepKeyHash, Hash: common.Blake2b224(e.co.Ring.DRep(id))}, voterDRep
}

// choiceNames are a vote's choices by common.GovVote value.
var choiceNames = map[uint8]string{common.GovVoteNo: "no", common.GovVoteYes: "yes", common.GovVoteAbstain: "abstain"}

func buildMint(ctx context.Context, e *Engine, w keys.Wallet, sp build.Spend, p *pendingRec) (build.Built, error) {
	q := int64(p.Amount)
	if p.Kind == string(profile.IntentBurn) {
		q = -q
	}
	return e.builder.Plutus().Mint(ctx, w, build.Mint{Spend: sp, Policy: build.FreeMint, Name: tokenName, Quantity: q, Redeemer: unitDatum()})
}

func buildUnlock(ctx context.Context, e *Engine, w keys.Wallet, sp build.Spend, p *pendingRec) (build.Built, error) {
	u, ok := e.win.View().Utxo(chain.Ref{Tx: chain.TxID(p.Locked.Tx), Index: p.Locked.Index})
	if !ok {
		return build.Built{}, fmt.Errorf("locked output %x#%d not in the view", p.Locked.Tx[:4], p.Locked.Index)
	}
	return e.builder.Plutus().Unlock(ctx, w, build.Unlock{Spend: sp, Script: build.Contract(p.Contract), Locked: u, Redeemer: unitDatum()})
}

func buildPool(ctx context.Context, e *Engine, w keys.Wallet, sp build.Spend, p *pendingRec) (build.Built, error) {
	id := population.ActorID(p.Actor)
	cold, err := e.co.Ring.PoolSigner(id)
	if err != nil {
		return build.Built{}, err
	}
	ks, i := e.co.Ring.PoolKeys(id)
	pk, err := ks.Pool(i, 0)
	if err != nil {
		return build.Built{}, err
	}
	pledge, cost, margin := e.poolTerms(id, p.ID)
	reg := build.PoolRegistration{
		Spend: sp, Cold: cold, VRFKeyHash: pk.VRFVKeyHash, Pledge: pledge, Cost: cost, Margin: margin,
	}
	if p.Update || p.Kind == string(profile.IntentUpdatePool) {
		return e.builder.Pool().UpdatePool(ctx, w, reg)
	}
	return e.builder.Pool().RegisterPool(ctx, w, reg)
}

func unitDatum() common.Datum { return common.Datum{Data: data.NewConstr(0)} }

// actorDatum marks a locked output with the actor that locked it, so the
// actor unlocks only its own.
func actorDatum(id population.ActorID) common.Datum {
	return common.Datum{Data: data.NewConstr(0, data.NewInteger(new(big.Int).SetUint64(uint64(id))))}
}

func datumKey(d common.Datum) []byte {
	raw, err := cbor.Encode(d)
	if err != nil {
		return nil
	}
	return raw
}

// kindOf is the spec key of a pending intent; a payment has none.
func (p *pendingRec) kindOf() string {
	if p.Kind == "" {
		return string(profile.IntentSend)
	}
	return p.Kind
}

// intend turns the chosen intent into a pending record and starts it. A
// kind with no transaction (idle, go_dark, go_live) never reaches it.
func (e *Engine) intend(ctx context.Context, at sched.Time, a population.Actor, st *actor, in agenda.Intent, gov build.ActionType, r *rand.Rand) error {
	p, why := e.prepare(at, a, in, r)
	if p != nil {
		p.GovType = string(gov)
	}
	if why == "" {
		var err error
		if why, err = e.start(ctx, at, p); err != nil {
			return err
		}
	}
	if why != "" {
		e.jr.Emit(at, KindSkipped, a.ID, Skipped{Kind: string(in.Kind), Why: why})
	}
	return nil
}

// prepare fills in a pending record for in, or says why it cannot be made.
func (e *Engine) prepare(at sched.Time, a population.Actor, in agenda.Intent, r *rand.Rand) (*pendingRec, string) {
	p := &pendingRec{Actor: uint64(a.ID), Kind: string(in.Kind), To: uint64(in.Target)}
	switch in.Kind {
	case profile.IntentSend:
		return e.prepareSend(a, p, r)
	case profile.IntentRegisterStake:
		// The view shows only registrations in blocks: a second one sent
		// beside one in flight fails StakeKeyRegisteredDELEG.
		if e.win.View().StakeRegistered(a.ID) || e.registering(a.ID) {
			return nil, "stake key registered or being registered"
		}
	case profile.IntentRegisterDRep:
		if e.win.View().DRepRegistered(a.ID) || e.inFlight(a.ID, string(profile.IntentRegisterDRep)) {
			return nil, "DRep registered or being registered"
		}
	case profile.IntentDelegate:
		if _, ok := e.co.Ring.PoolOf(in.Target); !ok {
			return nil, fmt.Sprintf("actor %d operates no pool", in.Target)
		}
		if e.registering(a.ID) {
			return nil, "stake key being registered"
		}
		p.Register = !e.win.View().StakeRegistered(a.ID)
	case profile.IntentDelegateDRep:
		if !e.win.View().DRepRegistered(in.Target) {
			return nil, fmt.Sprintf("actor %d is no registered DRep", in.Target)
		}
	case profile.IntentVote:
		ref, err := parseProposal(in.Proposal)
		if err != nil {
			return nil, err.Error()
		}
		p.Action, p.Choice = &ref, voteChoice(r)
		voter, _ := e.voterOf(a.ID)
		p.Power = e.win.View().VotingPower(voter)
	case profile.IntentMint:
		p.Amount = mintQuantity
	case profile.IntentBurn:
		if p.Amount = e.tokens(a.ID); p.Amount == 0 {
			return nil, "nothing to burn"
		}
	case profile.IntentRegisterPool:
		if _, ok := e.co.Ring.PoolOf(a.ID); !ok {
			return nil, "actor operates no pool"
		}
		p.Update = e.win.View().PoolRegistered(a.ID)
	case profile.IntentUpdatePool:
		if !e.win.View().PoolRegistered(a.ID) {
			return nil, "pool not registered"
		}
	case profile.IntentRetireDRep:
		if !e.win.View().DRepRegistered(a.ID) || e.inFlight(a.ID, string(profile.IntentRetireDRep)) {
			return nil, "DRep not registered or retiring"
		}
	case profile.IntentScriptCall:
		e.prepareScript(a.ID, p, r)
	case profile.IntentRetirePool:
		p.Epoch = e.cfg.Slots.EpochAt(at) + 1
	case profile.IntentWithdraw:
		// Conway refuses a withdrawal from an account with no vote
		// delegation (ConwayWdrlNotDelegatedToDRep).
		if !e.win.View().VoteDelegated(a.ID) {
			return nil, "withdrawal needs a vote delegation"
		}
	}
	if _, ok := specs[p.kindOf()]; !ok {
		return nil, "no builder for " + string(in.Kind)
	}
	return p, ""
}

// registering reports whether an unfinished intent of the actor registers
// its stake key.
func (e *Engine) registering(id population.ActorID) bool {
	for _, q := range e.pending {
		if q.Actor == uint64(id) && (q.Kind == string(profile.IntentRegisterStake) || q.Kind == string(profile.IntentDelegate) && q.Register) {
			return true
		}
	}
	return false
}

// inFlight reports whether an unfinished intent of the actor has kind.
func (e *Engine) inFlight(id population.ActorID, kind string) bool {
	for _, q := range e.pending {
		if q.Actor == uint64(id) && q.Kind == kind {
			return true
		}
	}
	return false
}

func (e *Engine) prepareSend(a population.Actor, p *pendingRec, r *rand.Rand) (*pendingRec, string) {
	var targets []population.ActorID
	for _, id := range e.co.IDs() {
		// Nobody pays an adversary.
		if id == a.ID || e.isAdversary(id) {
			continue
		}
		if t, _ := e.co.Get(id); t.State != population.Abandoned {
			targets = append(targets, id)
		}
	}
	if len(targets) == 0 {
		return nil, "no payee"
	}
	p.Kind, p.To = "", uint64(e.payee(a, targets, r))
	bal := e.Spendable(uint64(a.ID))
	floor := e.builder.MinPayment(e.co.Wallet(population.ActorID(p.To)).Address())
	p.Amount = paymentSize(e.co.Rec(a.ID).Prof.Amount, bal, floor, r)
	return p, ""
}

// payee picks whom a payment goes to. With probability the actor's care it
// pays one of the counterparties it paid before, if one is still around:
// the fitted care is the share of a unit's outputs that pay an address it
// paid before (lab/population_fit.py). Otherwise anyone in targets.
func (e *Engine) payee(a population.Actor, targets []population.ActorID, r *rand.Rand) population.ActorID {
	var known []population.ActorID
	for _, id := range e.co.Rec(a.ID).Paid {
		if _, ok := slices.BinarySearch(targets, id); ok {
			known = append(known, id)
		}
	}
	if len(known) > 0 && r.Float64() < a.Traits.Care {
		return known[r.IntN(len(known))]
	}
	return targets[r.IntN(len(targets))]
}

// prepareScript unlocks the actor's oldest locked output not already being
// unlocked, contract by contract, or locks a new one at a contract drawn
// by usage (pickContract).
func (e *Engine) prepareScript(id population.ActorID, p *pendingRec, r *rand.Rand) {
	busy := map[Ref]bool{}
	for _, q := range e.pending {
		// An honest actor knows its own unlocks, not an adversary's.
		if q.Locked != nil && q.Attack == "" {
			busy[*q.Locked] = true
		}
	}
	want := datumKey(actorDatum(id))
	for c := range e.contracts {
		for _, u := range e.win.View().UtxosAt(build.Contract(c).Address(e.cfg.NetworkID)) {
			d := u.Output.Datum()
			ref := Ref{Tx: TxID(u.Id.Id()), Index: u.Id.Index()}
			if d == nil || busy[ref] || !bytes.Equal(datumKey(*d), want) {
				continue
			}
			p.Kind, p.Locked, p.Amount, p.Contract = unlockKind, &ref, u.Output.Amount().Uint64(), c
			return
		}
	}
	p.Kind, p.Amount, p.Contract = lockKind, lockAmount, e.pickContract(r)
}

// tokens is the quantity of the mint token the actor holds at the tip.
func (e *Engine) tokens(id population.ActorID) uint64 {
	policy := build.FreeMint.Hash()
	var sum uint64
	for _, u := range e.win.View().UtxosAt(e.co.Wallet(id).Address()) {
		if as := u.Output.Assets(); as != nil {
			if q := as.Asset(common.Blake2b224(policy), tokenName); q != nil {
				sum += q.Uint64()
			}
		}
	}
	return sum
}

// voteChoice is an honest voter's choice: mostly yes.
func voteChoice(r *rand.Rand) uint8 {
	switch x := r.Float64(); {
	case x < 0.7:
		return common.GovVoteYes
	case x < 0.9:
		return common.GovVoteNo
	}
	return common.GovVoteAbstain
}

// parseProposal reads agenda.ProposalID's "txhash#index".
func parseProposal(id agenda.ProposalID) (Ref, error) {
	h, idx, ok := strings.Cut(string(id), "#")
	raw, err := hex.DecodeString(h)
	if !ok || err != nil || len(raw) != len(TxID{}) {
		return Ref{}, fmt.Errorf("malformed proposal id %q", id)
	}
	n, err := strconv.ParseUint(idx, 10, 32)
	if err != nil {
		return Ref{}, fmt.Errorf("malformed proposal id %q", id)
	}
	return Ref{Tx: TxID(raw), Index: uint32(n)}, nil
}

// start builds and reserves the first attempt of p and starts its retry
// machine. It returns why the intent was skipped, if it was.
func (e *Engine) start(ctx context.Context, at sched.Time, p *pendingRec) (string, error) {
	id := e.nextID + 1
	p.ID = id
	built, inputs, ttl, why, err := e.attemptOf(ctx, at, p)
	if why != "" || err != nil {
		return why, err
	}
	if err := e.led.Reserve(ctx, plan.IntentID(id), attempt(population.ActorID(p.Actor), TxID(built.ID), inputs, ttl)); err != nil {
		return "reserve: " + err.Error(), nil
	}
	e.nextID = id
	p.Tx, p.TTL, p.Cbor, p.Awaiting, p.Txs = TxID(built.ID), ttl, built.Tx, true, []TxID{TxID(built.ID)}
	if err := e.begin(p); err != nil {
		return "", err
	}
	e.addPending(p)
	if p.Kind == "" && !p.Refill {
		if st := e.co.Rec(population.ActorID(p.Actor)); st != nil {
			st.Remember(population.ActorID(p.To))
		}
	}
	e.market.excite(at)
	e.emitSubmitted(at, p, built, inputs)
	return "", e.react(ctx, at, p, built.Fee)
}

func (e *Engine) emitSubmitted(at sched.Time, p *pendingRec, built build.Built, inputs []Ref) {
	attack := p.Attack
	if attack == "" && e.isAdversary(population.ActorID(p.Actor)) {
		attack = coverTraffic
	}
	s := Submitted{
		Intent: p.ID, ID: TxID(built.ID), To: p.To, Amount: p.Amount, Fee: built.Fee, TTL: p.TTL, Inputs: inputs, Tx: built.Tx, Kind: p.Kind,
		Mistakes: p.Carried, Attack: attack, Action: p.GovType, Contract: p.Contract,
	}
	if p.Kind == string(profile.IntentVote) && p.Action != nil {
		_, s.Voter = e.voterOf(population.ActorID(p.Actor))
		s.Proposal = chain.GovID{Tx: chain.TxID(p.Action.Tx), Index: p.Action.Index}.String()
		s.Choice, s.Power = choiceNames[p.Choice], p.Power
	}
	e.jr.Emit(at, KindSubmitted, population.ActorID(p.Actor), s)
}

// attemptOf builds one attempt of p on inputs the planner offers its
// intent. A failed build is a skip reason, not an error.
func (e *Engine) attemptOf(ctx context.Context, at sched.Time, p *pendingRec) (build.Built, []Ref, uint64, string, error) {
	sp := specs[p.kindOf()]
	a := population.ActorID(p.Actor)
	avail := e.coins(a, plan.IntentID(p.ID))
	need := sp.need(e, p) + sp.headroom
	coins := e.sel.Select(avail, need)
	if coins == nil {
		return build.Built{}, nil, 0, fmt.Sprintf("spendable %d below %d", sumCoins(avail), need), nil
	}
	ttl := e.ttl(at, sp.guarded, sp.script)
	if ttl == 0 {
		return build.Built{}, nil, 0, "too close to the epoch boundary", nil
	}
	w := e.co.Wallet(a)
	// Only the first attempt is built before any send; it samples the
	// intent's mistake.
	first := p.M.Attempts == 0
	attempt := func() (build.Built, error) {
		b, err := sp.build(ctx, e, w, build.Spend{Inputs: e.ledgerUtxos(w, coins), TTL: ttl}, p)
		if err != nil {
			return b, err
		}
		if first {
			e.sample(p, w, b)
			first = false
		}
		return e.mutate(p, w, b)
	}
	built, err := attempt()
	// A rebuild at the slot of the attempt it replaces, on the same inputs,
	// is the same transaction; a later TTL makes it a new one.
	for _, ok := e.byTx[TxID(built.ID)]; err == nil && ok; _, ok = e.byTx[TxID(built.ID)] {
		ttl++
		built, err = attempt()
	}
	if err != nil {
		if ctx.Err() != nil {
			return build.Built{}, nil, 0, "", ctx.Err()
		}
		return build.Built{}, nil, 0, "build: " + err.Error(), nil
	}
	inputs := make([]Ref, len(coins))
	for i, c := range coins {
		inputs[i] = c.Ref
	}
	return built, inputs, ttl, "", nil
}

// ttl is an attempt's invalid-hereafter slot. A guarded step's ends with
// the current epoch, so the epoch its effect reaches is known when the
// guard runs; zero means too little of the epoch is left.
func (e *Engine) ttl(at sched.Time, guarded, script bool) uint64 {
	// An overdue event runs at its own time, but its attempt is sent at
	// the Tick's (tickTo), so the interval starts there; a rebuild runs at
	// a block's time, which can lag the block's own slot.
	at = max(at, e.clk.TickTo())
	slot := max(e.cfg.Slots.SlotAt(at), e.Tip().Slot)
	ttl := slot + e.cfg.TTL
	if script && e.cfg.Horizon > 1 {
		// The node's tip may trail slot; half the window leaves it room.
		ttl = min(ttl, e.Tip().Slot+e.cfg.Horizon/2)
	}
	if !guarded {
		return ttl
	}
	end := e.cfg.Slots.SlotAt(e.cfg.Slots.EpochStart(e.cfg.Slots.EpochAt(at) + 1))
	ttl = min(ttl, end)
	if ttl <= slot+1 {
		return 0
	}
	return ttl
}
