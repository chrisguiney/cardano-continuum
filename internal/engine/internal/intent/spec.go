package intent

import (
	"context"
	"fmt"
	"math"
	"math/big"
	"strconv"

	"github.com/blinklabs-io/gouroboros/cbor"
	"github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/plutigo/data"
	"github.com/chrisguiney/cardano-continuum/internal/agenda"
	"github.com/chrisguiney/cardano-continuum/internal/build"
	"github.com/chrisguiney/cardano-continuum/internal/chain"
	"github.com/chrisguiney/cardano-continuum/internal/contract"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/wire"
	"github.com/chrisguiney/cardano-continuum/internal/fakechain"
	"github.com/chrisguiney/cardano-continuum/internal/grants"
	"github.com/chrisguiney/cardano-continuum/internal/keys"
	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
	"github.com/chrisguiney/cardano-continuum/internal/protocol"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
	"github.com/chrisguiney/cardano-continuum/internal/submit"
)

// in what the intent names only by kind (payee, pool, proposal, locked
// output), the planner reserves the inputs, the builder for the kind signs
// the attempt, and the retry machine drives it. A spec is a kind's builder
// and what its inputs must cover.

// Intent kinds that are not agenda kinds: script_call is a lock or an
// unlock, decided by whether the actor has something locked.
const (
	LockKind   = "lock"
	UnlockKind = "unlock"
)

const (
	// ScriptHeadroom covers a script transaction's fee and collateral, which
	// must come from one pure-ada input.
	ScriptHeadroom = 6_000_000
	lockAmount     = 5_000_000
	mintQuantity   = 1000
)

// tokenName is what every mint names under build.FreeMint.
var tokenName = []byte("continuum")

type kind struct {
	// need is the lovelace the inputs cover besides headroom.
	need     func(c *core, p *wire.Pending) uint64
	headroom uint64
	build    func(ctx context.Context, c *core, w keys.Wallet, sp build.Spend, p *wire.Pending) (build.Built, error)
	// deposit is the lovelace a settled attempt moves into the ledger's
	// deposits (negative: refunded), for the supply check.
	deposit func(c *core, p *wire.Pending) int64
	// guarded kinds can lower forging stake: their TTL ends inside the
	// current epoch (stake-floor rule 10).
	guarded bool
	// closing, when set, reports an attempt that must land in the epoch
	// it is built in, which guards it as guarded does.
	closing func(c *core, at sched.Time, p *wire.Pending) bool
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

func none(*core, *wire.Pending) uint64 { return 0 }

func noDeposit(*core, *wire.Pending) int64 { return 0 }

// payment is a plain payment of p.Amount to p.To: a send, an exchange's
// payout of a buy, a wallet's deposit of a sale.
var payment = kind{need: func(_ *core, p *wire.Pending) uint64 { return p.Amount }, headroom: FeeHeadroom, deposit: noDeposit,
	build: func(ctx context.Context, c *core, w keys.Wallet, sp build.Spend, p *wire.Pending) (build.Built, error) {
		return c.spec.Builder.Pay(ctx, w, build.Payment{Inputs: sp.Inputs, TTL: sp.TTL, To: c.env.Roster.Wallet(population.ActorID(p.To)).Address(), Amount: p.Amount})
	}}

var specs = map[string]kind{
	string(profile.IntentSend):    payment,
	string(profile.IntentBuyADA):  payment,
	string(profile.IntentSellADA): payment,
	string(profile.IntentRegisterStake): {need: func(c *core, _ *wire.Pending) uint64 { return c.spec.Build.KeyDeposit }, headroom: FeeHeadroom, again: againRegister,
		deposit: func(c *core, _ *wire.Pending) int64 { return int64(c.spec.Build.KeyDeposit) },
		build: func(ctx context.Context, c *core, w keys.Wallet, sp build.Spend, _ *wire.Pending) (build.Built, error) {
			return c.spec.Builder.Stake().RegisterStake(ctx, w, sp)
		}},
	string(profile.IntentDelegate): {need: registerNeed, headroom: FeeHeadroom, guarded: true, deposit: registerDeposit, again: againRegister,
		build: func(ctx context.Context, c *core, w keys.Wallet, sp build.Spend, p *wire.Pending) (build.Built, error) {
			pool, _ := c.env.Ring.PoolOf(population.ActorID(p.To))
			return c.spec.Builder.Stake().Delegate(ctx, w, build.Delegation{Spend: sp, Pool: pool, Register: p.Register})
		}},
	string(profile.IntentUndelegate): {need: none, headroom: FeeHeadroom, guarded: true, again: againDeregister,
		deposit: func(c *core, _ *wire.Pending) int64 { return -int64(c.spec.Build.KeyDeposit) },
		build: func(ctx context.Context, c *core, w keys.Wallet, sp build.Spend, _ *wire.Pending) (build.Built, error) {
			return c.spec.Builder.Stake().DeregisterStake(ctx, w, sp)
		}},
	string(profile.IntentWithdraw): {need: none, headroom: FeeHeadroom, deposit: noDeposit,
		again: []submit.Class{submit.Class(fakechain.WithdrawalsNotInRewards)},
		build: buildWithdraw},
	string(profile.IntentDelegateDRep): {need: none, headroom: FeeHeadroom, deposit: noDeposit,
		build: func(ctx context.Context, c *core, w keys.Wallet, sp build.Spend, p *wire.Pending) (build.Built, error) {
			cred := c.env.Ring.DRep(population.ActorID(p.To))
			d := common.Drep{Type: common.DrepTypeAddrKeyHash, Credential: cred[:]}
			return c.spec.Builder.Stake().DelegateVote(ctx, w, build.VoteDelegation{Spend: sp, DRep: d})
		}},
	string(profile.IntentRegisterDRep): {need: func(c *core, _ *wire.Pending) uint64 { return c.spec.Build.DRepDeposit }, headroom: FeeHeadroom,
		again:   []submit.Class{submit.Class(fakechain.DRepAlreadyRegistered)},
		deposit: func(c *core, _ *wire.Pending) int64 { return int64(c.spec.Build.DRepDeposit) },
		build: func(ctx context.Context, c *core, w keys.Wallet, sp build.Spend, p *wire.Pending) (build.Built, error) {
			return c.spec.Builder.Gov().RegisterDRep(ctx, w, build.DRepRegistration{Spend: sp, DRep: c.env.Ring.DRepSigner(population.ActorID(p.Actor))})
		}},
	string(profile.IntentVote):   {need: none, headroom: FeeHeadroom, closing: lastEpoch, deposit: noDeposit, build: buildVote},
	string(agenda.CommitteeVote): {need: none, headroom: FeeHeadroom, closing: lastEpoch, deposit: noDeposit, build: buildCommitteeVote},
	string(agenda.CommitteeAuth): {need: none, headroom: FeeHeadroom, deposit: noDeposit, build: buildCommitteeAuth},
	string(profile.IntentPropose): {need: func(c *core, _ *wire.Pending) uint64 { return c.spec.Build.GovActionDeposit }, headroom: FeeHeadroom,
		deposit: func(c *core, _ *wire.Pending) int64 { return int64(c.spec.Build.GovActionDeposit) }, build: buildPropose},
	string(profile.IntentRetireDRep): {need: none, headroom: FeeHeadroom,
		again:   []submit.Class{submit.Class(fakechain.DRepNotRegistered)},
		deposit: func(c *core, _ *wire.Pending) int64 { return -int64(c.spec.Build.DRepDeposit) },
		build: func(ctx context.Context, c *core, w keys.Wallet, sp build.Spend, p *wire.Pending) (build.Built, error) {
			return c.spec.Builder.Gov().RetireDRep(ctx, w, build.DRepRetirement{Spend: sp, DRep: c.env.Ring.DRepSigner(population.ActorID(p.Actor))})
		}},
	string(profile.IntentUpdatePool): {need: none, headroom: FeeHeadroom, deposit: noDeposit, build: buildPool},
	string(profile.IntentMint):       {need: none, headroom: ScriptHeadroom, deposit: noDeposit, build: buildMint, script: true},
	string(profile.IntentBurn):       {need: none, headroom: ScriptHeadroom, deposit: noDeposit, build: buildMint, script: true},
	LockKind: {need: func(_ *core, p *wire.Pending) uint64 { return p.Amount }, headroom: FeeHeadroom,
		deposit: func(_ *core, p *wire.Pending) int64 { return 0 },
		build:   buildLock},
	// A landed unlock's script input is gone, so its redeemer and script
	// witness point at nothing.
	UnlockKind: {need: none, headroom: ScriptHeadroom, deposit: noDeposit, build: buildUnlock, script: true,
		again: []submit.Class{submit.Class(fakechain.ExtraRedeemers), submit.MissingScriptWitnesses}},
	string(profile.IntentRegisterPool): {need: poolNeed, headroom: FeeHeadroom, deposit: poolDeposit, build: buildPool},
	string(profile.IntentRetirePool): {need: none, headroom: FeeHeadroom, guarded: true, deposit: noDeposit,
		build: func(ctx context.Context, c *core, w keys.Wallet, sp build.Spend, p *wire.Pending) (build.Built, error) {
			cold, err := c.env.Ring.PoolSigner(population.ActorID(p.Actor))
			if err != nil {
				return build.Built{}, err
			}
			return c.spec.Builder.Pool().RetirePool(ctx, w, build.PoolRetirement{Spend: sp, Cold: cold, Epoch: p.Epoch, CurrentEpoch: p.Epoch - 1})
		}},
}

// lastEpoch reports a vote on a proposal in its last epoch: one landing
// after the boundary is refused (VotingOnExpiredGovAction).
func lastEpoch(c *core, at sched.Time, p *wire.Pending) bool {
	if p.Action == nil {
		return false
	}
	id := agenda.ProposalID(chain.GovID{Tx: chain.TxID(p.Action.Tx), Index: p.Action.Index}.String())
	for _, o := range c.env.Chain.View().OpenProposals() {
		if o.ID == id {
			return o.Expires <= c.spec.Slots.EpochAt(at)
		}
	}
	return true
}

func registerNeed(c *core, p *wire.Pending) uint64 {
	if p.Register {
		return c.spec.Build.KeyDeposit
	}
	return 0
}

func registerDeposit(c *core, p *wire.Pending) int64 { return int64(registerNeed(c, p)) }

func poolNeed(c *core, p *wire.Pending) uint64 {
	if p.Update {
		return 0
	}
	return c.spec.Build.PoolDeposit
}

func poolDeposit(c *core, p *wire.Pending) int64 { return int64(poolNeed(c, p)) }

func buildVote(ctx context.Context, c *core, w keys.Wallet, sp build.Spend, p *wire.Pending) (build.Built, error) {
	act := common.GovActionId{TransactionId: common.Blake2b256(p.Action.Tx), GovActionIdx: p.Action.Index}
	id := population.ActorID(p.Actor)
	v := build.Vote{Spend: sp, Voter: c.env.Ring.DRepSigner(id), Role: build.RoleDRep, Action: act, Choice: p.Choice}
	if _, ok := c.env.Ring.PoolOf(id); ok {
		cold, err := c.env.Ring.PoolSigner(id)
		if err != nil {
			return build.Built{}, err
		}
		v.Voter, v.Role = cold, build.RolePool
	}
	return c.spec.Builder.Gov().Vote(ctx, w, v)
}

// buildCommitteeVote casts seat p.To's vote with its hot key, which an
// earlier committee_auth has authorised, or, with p.Auth, which the vote
// authorises itself (from protocol version 11).
func buildCommitteeVote(ctx context.Context, c *core, w keys.Wallet, sp build.Spend, p *wire.Pending) (build.Built, error) {
	act := common.GovActionId{TransactionId: common.Blake2b256(p.Action.Tx), GovActionIdx: p.Action.Index}
	cold, hot := c.env.Ring.Committee(uint32(p.To))

	var auth *keys.Signer
	if p.Auth {
		auth = &cold
	}
	return c.spec.Builder.Gov().Vote(ctx, w, build.Vote{
		Spend:  sp,
		Voter:  hot,
		Auth:   auth,
		Role:   build.RoleCommittee,
		Action: act,
		Choice: p.Choice,
	})
}

// buildCommitteeAuth authorises seat p.To's hot key with its cold key.
func buildCommitteeAuth(ctx context.Context, c *core, w keys.Wallet, sp build.Spend, p *wire.Pending) (build.Built, error) {
	cold, hot := c.env.Ring.Committee(uint32(p.To))
	return c.spec.Builder.Gov().AuthorizeHot(ctx, w, build.HotAuth{Spend: sp, Cold: cold, Hot: hot.KeyHash()})
}

// buildPropose submits the proposal; a parameter change is the one the
// plan gives (planParams), or none while one is open.
func buildPropose(ctx context.Context, c *core, w keys.Wallet, sp build.Spend, p *wire.Pending) (build.Built, error) {
	prop := build.Proposal{Spend: sp, Action: build.ActionType(p.GovType)}
	if prop.Action == build.ActionTreasuryWithdrawals {
		prop.Payees = treasuryPayees(c, p)
	}
	if prop.Action == build.ActionHardFork {
		prop.Fork = hardFork(c.env.Chain.View(), p.Fork)
	}
	if prop.Action == build.ActionParameterChange {
		pc, ok := planParams(paramLinks(c.env.Chain.View()))
		if !ok {
			return build.Built{}, errParamOpen
		}
		prop.Change = &build.ParamChange{Update: pc.update}
		if pc.prev != nil {
			prop.Change.Prev = &common.GovActionId{TransactionId: common.Blake2b256(pc.prev.Tx), GovActionIdx: pc.prev.Index}
		}
	}
	return c.spec.Builder.Gov().Propose(ctx, w, prop)
}

// hardFork is the hard fork to version to (nil: none, which the builder
// refuses) from the view's version in force, after the view's hard-fork
// root, the last fork it ratified (models/specs/governance-change
// HardFork.tla, ProposeFork).
func hardFork(v *chain.View, to *protocol.Version) *build.HardFork {
	if to == nil {
		return nil
	}

	h := &build.HardFork{From: v.VersionAt(v.Epoch()), To: *to}
	if fs := v.Forks(); len(fs) > 0 {
		last := fs[len(fs)-1].ID
		h.Prev = &common.GovActionId{TransactionId: common.Blake2b256(last.Tx), GovActionIdx: last.Index}
	}
	return h
}

func buildMint(ctx context.Context, c *core, w keys.Wallet, sp build.Spend, p *wire.Pending) (build.Built, error) {
	q := int64(p.Amount)
	if p.Kind == string(profile.IntentBurn) {
		q = -q
	}
	return c.spec.Builder.Plutus().Mint(ctx, w, build.Mint{Spend: sp, Policy: build.FreeMint, Name: tokenName, Quantity: q, Redeemer: UnitDatum()})
}

// buildLock locks at contract p.Contract with the datum its template
// takes: a vesting position opens vestDelay slots after the tip, and an
// escrow pays p.To, refundable escrowWindow slots after the tip.
func buildLock(ctx context.Context, c *core, w keys.Wallet, sp build.Spend, p *wire.Pending) (build.Built, error) {
	template, tip := build.Template(p.Contract), c.env.Chain.Tip().Slot
	until := c.spec.Builder.SlotTime(tip + vestDelay)

	var counterparty keys.Credential
	if template == "escrow" {
		until = c.spec.Builder.SlotTime(tip + escrowWindow)
		counterparty = c.env.Roster.Wallet(population.ActorID(p.To)).PaymentKeyHash()
	}

	d := lockDatum(template, population.ActorID(p.Actor), w.PaymentKeyHash(), counterparty, until)
	return c.spec.Builder.Plutus().Lock(ctx, w, build.Lock{Spend: sp, Script: build.Contract(p.Contract), Amount: p.Amount, Datum: d})
}

// buildUnlock makes p.Call on p.Locked as its contract's template
// requires; a release carries the signature of its depositor, p.To.
func buildUnlock(ctx context.Context, c *core, w keys.Wallet, sp build.Spend, p *wire.Pending) (build.Built, error) {
	u, ok := c.env.Chain.View().Utxo(chain.Ref{Tx: chain.TxID(p.Locked.Tx), Index: p.Locked.Index})
	if !ok {
		return build.Built{}, fmt.Errorf("locked output %x#%d not in the view", p.Locked.Tx[:4], p.Locked.Index)
	}

	cl := claimArgs(build.Template(p.Contract), p.Call, w.PaymentKeyHash(), c.env.Chain.Tip().Slot)

	var co []keys.Signer
	if p.Call == callRelease || p.Call == callDraw {
		co = []keys.Signer{c.env.Roster.Wallet(population.ActorID(p.To)).PaymentSigner()}
	}

	var rest uint64
	var restDatum common.Datum
	if a, ok := contract.ParseDisbursementDatum(u.Output.Datum().Data); ok && p.Call == callDraw {
		rest, restDatum = drawRest(a, u.Output.Amount().Uint64())
	}
	return c.spec.Builder.Plutus().Unlock(ctx, w, build.Unlock{
		Spend:     sp,
		Script:    build.Contract(p.Contract),
		Locked:    u,
		Redeemer:  cl.redeemer,
		Signers:   cl.signers,
		CoSigners: co,
		From:      cl.from,
		Rest:      rest,
		RestDatum: restDatum,
	})
}

// treasuryPayees are what proposal p's treasury withdrawal pays: the
// treasury recipients registered to receive, drawn by grants.Draw from a
// stream named by the intent against the treasury the view reckons.
// Without any registered, the proposal asks for its proposer.
func treasuryPayees(c *core, p *wire.Pending) []build.Payee {
	v := c.env.Chain.View()
	byProfile := map[string]population.ActorID{}
	for _, id := range c.env.Roster.IDs() {
		prof := c.env.Roster.Profile(id)
		if prof == nil || !v.StakeRegistered(id) {
			continue
		}

		if _, ok := grants.ByProfile(prof.Name); ok {
			byProfile[prof.Name] = id
		}
	}

	r := c.spec.Seed.Derive("grants", strconv.FormatUint(p.ID, 10)).Rand()
	var out []build.Payee
	for _, a := range grants.Draw(r, v.Treasury(), func(name string) bool { _, ok := byProfile[name]; return ok }) {
		out = append(out, build.Payee{To: c.env.Roster.Wallet(byProfile[a.Kind]).StakeAddress(), Amount: a.Lovelace})
	}
	return out
}

// buildWithdraw withdraws the reward balance p.Amount; an award
// (callAward) goes straight to the disbursement contract, drawable in
// milestones tranches from milestoneEvery slots on, overseen by p.To.
func buildWithdraw(ctx context.Context, c *core, w keys.Wallet, sp build.Spend, p *wire.Pending) (build.Built, error) {
	wd := build.Withdrawal{Spend: sp, Amount: p.Amount}
	if p.Call == callAward {
		me, admin := w.PaymentKeyHash(), c.env.Roster.Wallet(population.ActorID(p.To)).PaymentKeyHash()
		period := c.spec.Builder.SlotTime(milestoneEvery) - c.spec.Builder.SlotTime(0)
		d := contract.DisbursementDatum{
			Beneficiary:   me[:],
			Administrator: admin[:],
			Next:          c.spec.Builder.SlotTime(c.env.Chain.Tip().Slot + milestoneEvery),
			Period:        period,
			// Rounding the tranche up leaves the last draw at most one
			// tranche, which the validator lets it take whole.
			Tranche: int64((p.Amount + milestones - 1) / milestones),
		}
		wd.Into, wd.Datum = &build.Disbursement, common.Datum{Data: d.Data()}
	}
	return c.spec.Builder.Stake().Withdraw(ctx, w, wd)
}

func buildPool(ctx context.Context, c *core, w keys.Wallet, sp build.Spend, p *wire.Pending) (build.Built, error) {
	id := population.ActorID(p.Actor)
	cold, err := c.env.Ring.PoolSigner(id)
	if err != nil {
		return build.Built{}, err
	}
	ks, i := c.env.Ring.PoolKeys(id)
	pk, err := ks.Pool(i, 0)
	if err != nil {
		return build.Built{}, err
	}
	pledge, cost, margin := poolTerms(c, id, p.ID)
	reg := build.PoolRegistration{
		Spend: sp, Cold: cold, VRFKeyHash: pk.VRFVKeyHash, Pledge: pledge, Cost: cost, Margin: margin,
	}
	if p.Update || p.Kind == string(profile.IntentUpdatePool) {
		return c.spec.Builder.Pool().UpdatePool(ctx, w, reg)
	}
	return c.spec.Builder.Pool().RegisterPool(ctx, w, reg)
}

func UnitDatum() common.Datum { return common.Datum{Data: data.NewConstr(0)} }

// ActorDatum marks a locked output with the actor that locked it, so the
// actor unlocks only its own.
func ActorDatum(id population.ActorID) common.Datum {
	return common.Datum{Data: data.NewConstr(0, data.NewInteger(new(big.Int).SetUint64(uint64(id))))}
}

func DatumKey(d common.Datum) []byte {
	raw, err := cbor.Encode(d)
	if err != nil {
		return nil
	}
	return raw
}

// poolTerms are the pledge, cost and margin a pool actor registers or
// updates with: draws from the population's fitted distributions on a
// stream named by the intent, so a rebuild offers the same terms. A pool
// of no population registers the minimum terms.
func poolTerms(c *core, id population.ActorID, intent uint64) (pledge, cost uint64, margin *big.Rat) {
	pledge, cost, margin = 1_000_000, c.spec.Build.MinPoolCost, big.NewRat(1, 100)
	st := c.env.Roster.Profile(id)
	if st == nil {
		return pledge, cost, margin
	}
	pop, ok := c.spec.Pool(st.Name)
	if !ok || pop.Pool == nil {
		return pledge, cost, margin
	}
	a, _ := c.env.Roster.Get(id)
	r := a.Stream("pool-terms", strconv.FormatUint(intent, 10))
	pledge = lovelaceOf(pop.Pool.Pledge.Sample(r))
	cost = max(lovelaceOf(pop.Pool.Cost.Sample(r)), c.spec.Build.MinPoolCost)
	// A margin is a rational on chain; millionths are finer than any pool
	// sets.
	m := min(max(pop.Pool.Margin.Sample(r), 0), 1)
	return pledge, cost, big.NewRat(int64(math.Round(m*1e6)), 1e6)
}

// maxTerm caps a drawn pledge or cost: a lognormal tail can pass the
// lovelace supply.
const maxTerm = 45_000_000_000_000_000

func lovelaceOf(x float64) uint64 {
	if math.IsNaN(x) || x <= 0 {
		return 0
	}
	return uint64(min(x, maxTerm))
}
