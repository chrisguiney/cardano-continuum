package intent

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math/rand/v2"
	"slices"
	"strconv"
	"strings"

	"github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/chrisguiney/cardano-continuum/internal/agenda"
	"github.com/chrisguiney/cardano-continuum/internal/build"
	"github.com/chrisguiney/cardano-continuum/internal/chain"
	"github.com/chrisguiney/cardano-continuum/internal/contract"
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/wire"
	"github.com/chrisguiney/cardano-continuum/internal/grants"
	"github.com/chrisguiney/cardano-continuum/internal/keys"
	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/profile"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

// Chooser fills in an intent's pending record from what the intent names
// only by kind, or says why none can be made.
type Chooser struct {
	core
	ct *Contracts
}

// Voter classes as Submitted names them.
const (
	voterDRep      = "drep"
	voterSPO       = "spo"
	voterCommittee = "committee"
)

// Voter is the voter an actor votes as: its pool, as an SPO, when it
// operates one, else its DRep key.
func Voter(ring Ring, id population.ActorID) (chain.Voter, string) {
	if pool, ok := ring.PoolOf(id); ok {
		return chain.Voter{Kind: common.VoterTypeStakingPoolKeyHash, Hash: pool}, voterSPO
	}
	return chain.Voter{Kind: common.VoterTypeDRepKeyHash, Hash: common.Blake2b224(ring.DRep(id))}, voterDRep
}

// choiceNames are a vote's choices by common.GovVote value.
var choiceNames = map[uint8]string{common.GovVoteNo: "no", common.GovVoteYes: "yes", common.GovVoteAbstain: "abstain"}

func (ch *Chooser) Prepare(at sched.Time, a population.Actor, in agenda.Intent, r *rand.Rand) (*wire.Pending, string) {
	p := &wire.Pending{Actor: uint64(a.ID), Args: wire.Args{Kind: string(in.Kind), To: uint64(in.Target)}}
	switch in.Kind {
	case profile.IntentSend, profile.IntentBuyADA, profile.IntentSellADA:
		return ch.preparePayment(at, a, p, r)
	case profile.IntentRegisterStake:
		// The view shows only registrations in blocks: a second one sent
		// beside one in flight fails StakeKeyRegisteredDELEG.
		if ch.env.Chain.View().StakeRegistered(a.ID) || ch.registering(a.ID) {
			return nil, "stake key registered or being registered"
		}
	case profile.IntentRegisterDRep:
		if ch.env.Chain.View().DRepRegistered(a.ID) || ch.env.Flight.Has(a.ID, string(profile.IntentRegisterDRep)) {
			return nil, "DRep registered or being registered"
		}
	case profile.IntentDelegate:
		if _, ok := ch.env.Ring.PoolOf(in.Target); !ok {
			return nil, fmt.Sprintf("actor %d operates no pool", in.Target)
		}
		if ch.registering(a.ID) {
			return nil, "stake key being registered"
		}
		p.Register = !ch.env.Chain.View().StakeRegistered(a.ID)
	case profile.IntentDelegateDRep:
		if !ch.env.Chain.View().DRepRegistered(in.Target) {
			return nil, fmt.Sprintf("actor %d is no registered DRep", in.Target)
		}
	case profile.IntentVote:
		ref, err := ParseProposal(in.Proposal)
		if err != nil {
			return nil, err.Error()
		}
		voter, class := Voter(ch.env.Ring, a.ID)
		p.Action, p.Choice = &ref, voteChoice(r, class, ref, ch.actionType(in.Proposal))
		p.Power = ch.env.Chain.View().VotingPower(voter)
	case agenda.CommitteeVote:
		ref, err := ParseProposal(in.Proposal)
		if err != nil {
			return nil, err.Error()
		}
		p.Action, p.Choice = &ref, voteChoice(r, voterCommittee, ref, ch.actionType(in.Proposal))
		// The pace sends an unauthorised seat's vote only where the chain
		// takes it with its authorisation (agenda.Committee JointAuth).
		p.Auth = !ch.env.Chain.View().HotAuthorized(int(in.Target))
	case profile.IntentMint:
		p.Amount = mintQuantity
	case profile.IntentBurn:
		if p.Amount = ch.tokens(a.ID); p.Amount == 0 {
			return nil, "nothing to burn"
		}
	case profile.IntentRegisterPool:
		if _, ok := ch.env.Ring.PoolOf(a.ID); !ok {
			return nil, "actor operates no pool"
		}
		p.Update = ch.env.Chain.View().PoolRegistered(a.ID)
	case profile.IntentUpdatePool:
		if !ch.env.Chain.View().PoolRegistered(a.ID) {
			return nil, "pool not registered"
		}
	case profile.IntentRetireDRep:
		if !ch.env.Chain.View().DRepRegistered(a.ID) || ch.env.Flight.Has(a.ID, string(profile.IntentRetireDRep)) {
			return nil, "DRep not registered or retiring"
		}
	case profile.IntentScriptCall:
		ch.prepareScript(a, p, r)
	case profile.IntentRetirePool:
		p.Epoch = ch.spec.Slots.EpochAt(at) + 1
	case profile.IntentWithdraw:
		// Conway refuses a withdrawal from an account with no vote
		// delegation (ConwayWdrlNotDelegatedToDRep).
		if !ch.env.Chain.View().VoteDelegated(a.ID) {
			return nil, "withdrawal needs a vote delegation"
		}
		ch.prepareAward(a.ID, p)
	}
	if _, ok := specs[p.KindOf()]; !ok {
		return nil, "no builder for " + string(in.Kind)
	}
	return p, ""
}

// registering reports whether an unfinished intent of the actor registers
// its stake key.
func (ch *Chooser) registering(id population.ActorID) bool {
	for q := range ch.env.Flight.All() {
		if q.Actor == uint64(id) && (q.Kind == string(profile.IntentRegisterStake) || q.Kind == string(profile.IntentDelegate) && q.Register) {
			return true
		}
	}
	return false
}

func (ch *Chooser) prepareSend(a population.Actor, p *wire.Pending, r *rand.Rand) (*wire.Pending, string) {
	var targets []population.ActorID
	for _, id := range ch.env.Roster.IDs() {
		// Nobody pays an adversary.
		if id == a.ID || ch.env.Spend.Adversary(id) {
			continue
		}
		if t, _ := ch.env.Roster.Get(id); t.State != population.Abandoned {
			targets = append(targets, id)
		}
	}
	if len(targets) == 0 {
		return nil, "no payee"
	}
	p.Kind, p.To = "", uint64(ch.payee(a, targets, r))
	bal := ch.env.Flight.Spendable(uint64(a.ID))
	floor := ch.spec.Builder.MinPayment(ch.env.Roster.Wallet(population.ActorID(p.To)).Address())
	p.Amount = paymentSize(ch.env.Roster.Profile(a.ID).Amount, bal, floor, r)
	return p, ""
}

// preparePayment prepares the kinds that are plain payments: a send, or a
// trade on an exchange.
func (ch *Chooser) preparePayment(at sched.Time, a population.Actor, p *wire.Pending, r *rand.Rand) (*wire.Pending, string) {
	if p.Kind == string(profile.IntentSend) {
		return ch.prepareSend(a, p, r)
	}
	return ch.prepareTrade(at, a, p, r)
}

// Trade sizing. A buy spends a drawn share of the wallet's budget in fiat,
// at least MinTrade; a sale sells a drawn share of the spendable balance,
// and a sale under water (Order.Gain below -panicGain) a far larger one.
const (
	MinTrade   = 5_000_000
	buyShare   = 0.02
	buySpread  = 0.08
	panicGain  = 0.1
	panicShare = 0.5
)

// prepareTrade quotes a buy or sale on the wallet's venue. A buy is the
// exchange's payment of what the wallet bought, less the withdrawal fee;
// a sale is the wallet's payment to the exchange.
func (ch *Chooser) prepareTrade(at sched.Time, a population.Actor, p *wire.Pending, r *rand.Rand) (*wire.Pending, string) {
	if ch.env.Market == nil {
		return nil, "no market"
	}
	buy := p.Kind == string(profile.IntentBuyADA)
	var (
		gross uint64
		able  func(population.ActorID) bool
	)
	if buy {
		// The exchange pays the buy, so only one with the ADA free can be
		// the venue, and it fills no more than it has free.
		gross = max(MinTrade, uint64(float64(ch.env.Roster.Profile(a.ID).Budget)*(buyShare+buySpread*r.Float64())))
		able = func(x population.ActorID) bool { return ch.env.Flight.Spendable(uint64(x)) >= MinTrade+FeeHeadroom }
	}
	o, venue, ok := ch.env.Market.Order(at, a.ID, buy, able, r)
	if !ok {
		if buy {
			return nil, "no exchange with free ADA quotes the wallet's currencies"
		}
		return nil, "no exchange quotes the wallet's currencies"
	}
	p.Order = &o

	if buy {
		gross = min(gross, ch.env.Flight.Spendable(uint64(venue))-FeeHeadroom)
		if o.Fee >= gross {
			return nil, "withdrawal fee exceeds the purchase"
		}
		p.Actor, p.To, p.Amount = uint64(venue), uint64(a.ID), gross-o.Fee
		return p, ""
	}

	bal := ch.env.Flight.Spendable(uint64(a.ID))
	floor := ch.spec.Builder.MinPayment(ch.env.Roster.Wallet(venue).Address())
	p.To = uint64(venue)
	p.Amount = paymentSize(nil, bal, floor, r)
	if o.Gain < -panicGain && bal > FeeHeadroom {
		p.Amount = max(floor, uint64(float64(bal-FeeHeadroom)*(panicShare+(1-panicShare)*r.Float64())))
	}
	return p, ""
}

// payee picks whom a payment goes to. With probability the actor's care it
// pays one of the counterparties it paid before, if one is still around:
// the fitted care is the share of a unit's outputs that pay an address it
// paid before (lab/population_fit.py). Otherwise anyone in targets.
func (ch *Chooser) payee(a population.Actor, targets []population.ActorID, r *rand.Rand) population.ActorID {
	var known []population.ActorID
	for _, id := range ch.env.Roster.Paid(a.ID) {
		if _, ok := slices.BinarySearch(targets, id); ok {
			known = append(known, id)
		}
	}
	if len(known) > 0 && r.Float64() < a.Traits.Care {
		return known[r.IntN(len(known))]
	}
	return targets[r.IntN(len(targets))]
}

// prepareScript makes the call the actor's role allows on the oldest
// locked output not already being unlocked (unlockable), or locks
// a new one at a contract drawn by usage (Contracts.pick). A vesting
// position not yet due, or an escrow paid to the actor that its depositor
// disputes, offers no call, so the actor locks again instead of idling.
// An escrow lock pays another script user (beneficiary).
func (ch *Chooser) prepareScript(a population.Actor, p *wire.Pending, r *rand.Rand) {
	if u, ok := ch.unlockable(a.ID); ok {
		p.Kind, p.Locked, p.Amount, p.Contract, p.Call, p.To = UnlockKind, &u.ref, u.amount, u.contract, u.call, uint64(u.to)
		return
	}

	p.Kind, p.Amount, p.Contract, p.Call = LockKind, lockAmount, ch.ct.pick(r), callLock
	if build.Template(p.Contract) != "escrow" {
		return
	}

	to, ok := ch.beneficiary(a, r)
	if !ok {
		// No other script user to pay: the shared lock instead.
		p.Contract = 0
		return
	}
	p.To = uint64(to)
}

// callPhrases describe each unlock call for an actor's why box.
var callPhrases = map[string]string{
	callClaim:   "claim a position of its own",
	callRelease: "release an escrow paid to it",
	callRefund:  "refund its escrow past the deadline",
	callDraw:    "draw a milestone of its treasury award",
}

// Calls describes the unlock a script call by actor id would make now,
// empty when it would lock (agenda.Positions).
func (ch *Chooser) Calls(id population.ActorID) string {
	u, ok := ch.unlockable(id)
	if !ok {
		return ""
	}
	return callPhrases[u.call] + " at " + build.Contract(u.contract).Name
}

// unlock is a call an actor may make on a locked output.
type unlock struct {
	// ref is the locked output and amount its lovelace.
	ref    wire.Ref
	amount uint64
	// contract is the contract it is locked at.
	contract uint64
	// call is what the actor does with it.
	call string
	// to is a release's depositor, who co-signs it; zero otherwise.
	to population.ActorID
}

// unlockable is the first call actor id may make, contract by contract
// and oldest output first, on an output no honest unlock in flight
// spends.
func (ch *Chooser) unlockable(id population.ActorID) (unlock, bool) {
	busy := map[wire.Ref]bool{}
	for q := range ch.env.Flight.All() {
		// An honest actor knows its own unlocks, not an adversary's.
		if q.Locked != nil && q.Attack == "" {
			busy[*q.Locked] = true
		}
	}

	// Every contract address is booked to the script owner, so one walk
	// of its outputs, grouped by contract, finds them all.
	at := map[common.Blake2b224][]common.Utxo{}
	for _, u := range ch.env.Chain.View().UtxosOf(ch.spec.Script) {
		h := u.Output.Address().PaymentKeyHash()
		at[h] = append(at[h], u)
	}

	for _, i := range append(slices.Collect(ch.ct.indexes()), build.DisburseContract) {
		for _, u := range at[build.Contract(i).Hash()] {
			ref := wire.Ref{Tx: wire.TxID(u.Id.Id()), Index: u.Id.Index()}
			if busy[ref] {
				continue
			}

			if c, ok := ch.callOn(id, i, u, ref); ok {
				return c, true
			}
		}
	}
	return unlock{}, false
}

// callOn is the call actor id may make on u, an output at contract i.
func (ch *Chooser) callOn(id population.ActorID, i uint64, u common.Utxo, ref wire.Ref) (unlock, bool) {
	owner := ch.env.Roster.Wallet(id).PaymentKeyHash()
	now := ch.spec.Builder.SlotTime(ch.env.Chain.Tip().Slot)
	call := callFor(build.Template(i), u.Output.Datum(), id, owner, now, approved(ref))
	if call == "" {
		return unlock{}, false
	}

	co, ok := ch.coSigner(call, u.Output.Datum())
	if !ok {
		return unlock{}, false
	}
	return unlock{ref: ref, amount: u.Output.Amount().Uint64(), contract: i, call: call, to: co}, true
}

// coSigner is the actor whose signature call needs beside the caller's:
// an escrow release's depositor, a draw's administrator; zero for none.
// ok is false when the datum names a party no actor holds.
func (ch *Chooser) coSigner(call string, d *common.Datum) (population.ActorID, bool) {
	var key []byte
	switch call {
	case callRelease:
		e, _ := contract.ParseEscrowDatum(d.Data)
		key = e.Depositor
	case callDraw:
		a, _ := contract.ParseDisbursementDatum(d.Data)
		key = a.Administrator
	default:
		return 0, true
	}

	if len(key) != len(keys.Credential{}) {
		return 0, false
	}
	return ch.env.Roster.Holder(keys.Credential(key))
}

// beneficiary picks whom an escrow of actor a pays: another script user
// (a profile that makes script calls, so it will claim), not an adversary
// and not abandoned, chosen as a payee is (payee).
func (ch *Chooser) beneficiary(a population.Actor, r *rand.Rand) (population.ActorID, bool) {
	var users []population.ActorID
	for _, id := range ch.env.Roster.IDs() {
		if id == a.ID || ch.env.Spend.Adversary(id) || !scriptUser(ch.env.Roster.Profile(id)) {
			continue
		}

		if t, _ := ch.env.Roster.Get(id); t.State != population.Abandoned {
			users = append(users, id)
		}
	}

	if len(users) == 0 {
		return 0, false
	}
	return ch.payee(a, users, r), true
}

// scriptUser reports a profile that makes script calls.
func scriptUser(p *profile.Profile) bool {
	return p != nil && (p.Weights[profile.IntentScriptCall] > 0 || p.Rates.Base[profile.IntentScriptCall] > 0)
}

// prepareAward makes a withdrawal take the whole reward balance, which
// the ledger requires. A treasury recipient that draws in milestones
// locks an award large enough for them at the disbursement contract under
// the governance administrator, named in p.To.
func (ch *Chooser) prepareAward(id population.ActorID, p *wire.Pending) {
	p.Amount = ch.env.Chain.View().Rewards(common.Blake2b224(ch.env.Roster.Wallet(id).StakeKeyHash()))
	prof := ch.env.Roster.Profile(id)
	if prof == nil || p.Amount < milestones*minTranche {
		return
	}

	k, ok := grants.ByProfile(prof.Name)
	if !ok || !k.Milestones {
		return
	}

	if admin, ok := ch.recipient(grants.Administrator); ok {
		p.Call, p.Contract, p.To = callAward, build.DisburseContract, uint64(admin)
	}
}

// recipient is the treasury recipient actor whose profile is name.
func (ch *Chooser) recipient(name string) (population.ActorID, bool) {
	for _, id := range ch.env.Roster.IDs() {
		if p := ch.env.Roster.Profile(id); p != nil && p.Name == name {
			return id, true
		}
	}
	return 0, false
}

// tokens is the quantity of the mint token the actor holds at the tip.
func (ch *Chooser) tokens(id population.ActorID) uint64 {
	policy := build.FreeMint.Hash()
	var sum uint64
	for _, u := range ch.env.Chain.View().UtxosAt(ch.env.Roster.Wallet(id).Address()) {
		if as := u.Output.Assets(); as != nil {
			if q := as.Asset(common.Blake2b224(policy), tokenName); q != nil {
				sum += q.Uint64()
			}
		}
	}
	return sum
}

// actionType is the type of the open proposal id, empty when it is not open.
func (ch *Chooser) actionType(id agenda.ProposalID) string {
	for _, p := range ch.env.Chain.View().OpenProposals() {
		if p.ID == id {
			return p.Type
		}
	}
	return ""
}

// A proposal is favoured or contested, the same for every voter, and each
// vote is drawn from its side's mix. Real votes agree per proposal (a
// treasury ask is popular or it is not), and independent draws at the
// overall mix would split every proposal near 69% yes, about the DRep
// threshold, so ratification would be a coin toss. The sides' mixes and
// each class's favoured share are set so the overall mix is mainnet's as
// mithril2pq measured it: DReps about 69% yes, 24% no and 9% abstain, pools
// about 82% yes.
var (
	// favouredMix and contestedMix are the yes, no and abstain shares of a
	// vote on a favoured and on a contested proposal.
	favouredMix  = [3]float64{0.9, 0.03, 0.07}
	contestedMix = [3]float64{0.1, 0.8, 0.1}
	// favoured is the share of proposals a voter class favours: yes is
	// 0.9q + 0.1(1-q), so q is (yes - 0.1) / 0.8.
	favoured = map[string]float64{voterDRep: 0.74, voterSPO: 0.9, voterCommittee: 0.9}
)

// alwaysFavoured are the action types every voter favours. The run's
// parameter changes are the network's own maintenance (a change and its
// revert), which a devnet must be able to enact; a hard fork comes only
// from the operator's hard_fork scenario, the network's planned upgrade.
var alwaysFavoured = map[string]bool{"parameter_change": true, "hard_fork_initiation": true}

// neverFavoured are the action types every voter contests, so they never
// enact. A no-confidence motion would remove the committee and with it
// every later ratification; a new constitution or a committee update would
// move a purpose root the proposer does not track, and its next proposal
// of that purpose would name a stale predecessor and be refused.
var neverFavoured = map[string]bool{"no_confidence": true, "new_constitution": true, "update_committee": true}

// voteChoice is an honest voter's choice on proposal id of type typ. The
// side comes from the proposal's hash, which is uniform, so every voter
// sees the same one; a class with a larger favoured share favours every
// proposal a smaller one does.
func voteChoice(r *rand.Rand, class string, id wire.Ref, typ string) uint8 {
	side := float64(binary.BigEndian.Uint32(id.Tx[:4])) / (1 << 32)
	m := contestedMix
	if !neverFavoured[typ] && (alwaysFavoured[typ] || side < favoured[class]) {
		m = favouredMix
	}
	switch x := r.Float64(); {
	case x < m[0]:
		return common.GovVoteYes
	case x < m[0]+m[1]:
		return common.GovVoteNo
	}
	return common.GovVoteAbstain
}

// ParseProposal reads agenda.ProposalID's "txhash#index".
func ParseProposal(id agenda.ProposalID) (wire.Ref, error) {
	h, idx, ok := strings.Cut(string(id), "#")
	raw, err := hex.DecodeString(h)
	if !ok || err != nil || len(raw) != len(wire.TxID{}) {
		return wire.Ref{}, fmt.Errorf("malformed proposal id %q", id)
	}
	n, err := strconv.ParseUint(idx, 10, 32)
	if err != nil {
		return wire.Ref{}, fmt.Errorf("malformed proposal id %q", id)
	}
	return wire.Ref{Tx: wire.TxID(raw), Index: uint32(n)}, nil
}

// Payment sizing. A payment sends a drawn fraction of the actor's spendable
// balance, never less than the payee output's min-UTxO, and needs
// FeeHeadroom on top so the builder can pay the fee and return change.
const (
	// FeeHeadroom is the lovelace beside a payment the inputs must cover.
	FeeHeadroom = 1_000_000
	minFraction = 0.05
	fracSpread  = 0.25
)

// paymentSize is the amount of a payment from an actor holding bal, never
// below floor, the min-UTxO of the payee's output (build.MinPayment).
// Without a profile distribution it is a drawn fraction of the balance.
// With one it is a draw from the distribution, capped so the payment leaves
// FeeHeadroom: an oversize draw pays what the wallet can rather than being
// skipped, which would bias the sizes of the payments that do go out
// toward the body.
func paymentSize(a *profile.Amount, bal, floor uint64, r *rand.Rand) uint64 {
	if a == nil {
		return max(uint64(float64(bal)*(minFraction+fracSpread*r.Float64())), floor)
	}
	ceiling := float64(bal) - FeeHeadroom
	if ceiling < float64(floor) {
		return floor
	}
	return max(uint64(min(a.Sample(r), ceiling)), floor)
}
