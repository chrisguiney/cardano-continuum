package intent

import (
	"bytes"
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
	"github.com/chrisguiney/cardano-continuum/internal/engine/internal/wire"
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
	voterDRep = "drep"
	voterSPO  = "spo"
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
	case profile.IntentSend:
		return ch.prepareSend(a, p, r)
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
		p.Action, p.Choice = &ref, voteChoice(r)
		voter, _ := Voter(ch.env.Ring, a.ID)
		p.Power = ch.env.Chain.View().VotingPower(voter)
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
		ch.prepareScript(a.ID, p, r)
	case profile.IntentRetirePool:
		p.Epoch = ch.spec.Slots.EpochAt(at) + 1
	case profile.IntentWithdraw:
		// Conway refuses a withdrawal from an account with no vote
		// delegation (ConwayWdrlNotDelegatedToDRep).
		if !ch.env.Chain.View().VoteDelegated(a.ID) {
			return nil, "withdrawal needs a vote delegation"
		}
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

// prepareScript unlocks the actor's oldest locked output not already being
// unlocked, contract by contract, or locks a new one at a contract drawn
// by usage (Contracts.pick).
func (ch *Chooser) prepareScript(id population.ActorID, p *wire.Pending, r *rand.Rand) {
	busy := map[wire.Ref]bool{}
	for q := range ch.env.Flight.All() {
		// An honest actor knows its own unlocks, not an adversary's.
		if q.Locked != nil && q.Attack == "" {
			busy[*q.Locked] = true
		}
	}
	want := DatumKey(ActorDatum(id))
	for i := range ch.ct.n {
		for _, u := range ch.env.Chain.View().UtxosAt(build.Contract(i).Address(ch.spec.NetworkID)) {
			d := u.Output.Datum()
			ref := wire.Ref{Tx: wire.TxID(u.Id.Id()), Index: u.Id.Index()}
			if d == nil || busy[ref] || !bytes.Equal(DatumKey(*d), want) {
				continue
			}
			p.Kind, p.Locked, p.Amount, p.Contract = UnlockKind, &ref, u.Output.Amount().Uint64(), uint64(i)
			return
		}
	}
	p.Kind, p.Amount, p.Contract = LockKind, lockAmount, ch.ct.pick(r)
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
