package chain

import (
	"math/big"
	"slices"

	"github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/gouroboros/ledger/conway"
)

// The thresholds the view ratifies parameter changes with, as the devnet
// genesis sets them (internal/genesis renderConway: dRepVotingThresholds,
// poolVotingThresholds and the committee's) and the fake chain's mainnet
// parameters have them. The view cannot read the node's thresholds from
// blocks; a run on a network with others judges its changes wrongly.
var (
	// drepGov is the governance group's DRep threshold, the highest; the
	// network, economic and technical groups share drepOther.
	drepGov   = big.NewRat(75, 100)
	drepOther = big.NewRat(67, 100)
	// poolSecurity is the pools' threshold on a security-group change.
	poolSecurity = big.NewRat(51, 100)
	// drepFork and poolFork are the DReps' and the pools' hard-fork
	// thresholds (hardForkInitiation, P4 and Q4).
	drepFork = big.NewRat(60, 100)
	poolFork = big.NewRat(51, 100)
	// committeeQuorum is the genesis committee threshold.
	committeeQuorum = big.NewRat(2, 3)
)

// govGroup reports that a parameter update touches the governance group
// (Conway PParamGroups). A change needs the highest DRep threshold of the
// groups it touches (the ParameterChange rule in Rules/Ratify.hs), and only
// the governance group's is higher than the others.
func govGroup(u conway.ConwayProtocolParameterUpdate) bool {
	gov := u.PoolVotingThresholds != nil || u.DRepVotingThresholds != nil || u.MinCommitteeSize != nil ||
		u.CommitteeTermLimit != nil || u.GovActionValidityPeriod != nil || u.GovActionDeposit != nil ||
		u.DRepDeposit != nil || u.DRepInactivityPeriod != nil
	return gov
}

// ratify enacts, at the boundary into epoch ep, every tracked parameter
// change not past its last voting boundary whose predecessor is the root
// and whose votes, weighed with the stake of now (the boundary, before the
// block's transactions), pass; in ID order, so a change naming one enacted
// here can follow it. It is the fake chain's ratify (internal/fakechain
// ratify.go), from the view's side: the engine never sees the ledger's
// enactments, so it repeats them from the votes and stake it folds. The
// ledger ratifies from the snapshot at this boundary and enacts at the
// next; the view takes the change out of the open list here, while votes
// on it are still accepted.
//
// Hard forks come first, as the ledger's priority order has them
// (formal-ledger-specifications f8059737f1db,
// src/Ledger/Conway/Specification/Gov.lagda.md govActionPriority), and a
// ratified hard fork delays everything after it at the same boundary
// (Ratify.lagda.md delayingAction, delayed): models/specs/governance-change
// HardFork.tla, rule 1.
//
// Treasury withdrawals come after parameter changes, as they do in
// govActionPriority, and the withdrawals enacted at the boundary before
// are paid first (payOut).
//
// Only the stake the book names counts, which on a devnet is all of it.
func ratify(v *View, ep uint64, u *undo) {
	payOut(v, u)
	if ratifyForks(v, ep, u) {
		return
	}

	ps := v.Proposals("parameter_change")
	slices.SortFunc(ps, func(a, b ProposalState) int { return a.ID.Compare(b.ID) })

	for _, p := range ps {
		if p.Enacted || p.Expires+1 < ep || !sameRoot(p.Prev, v.root) {
			continue
		}

		if passes(v, p) {
			v.enact(p.ID, u)
		}
	}
	ratifyWithdrawals(v, ep, u)
}

// ratifyForks ratifies, at the boundary into ep, the first open hard fork
// in ID order that names the hard-fork root and whose votes pass, and
// reports whether it ratified one. Its version is in force from the next
// boundary (gov.ratifyFork): HardFork.tla, rule 2.
func ratifyForks(v *View, ep uint64, u *undo) bool {
	ps := v.Proposals("hard_fork_initiation")
	slices.SortFunc(ps, func(a, b ProposalState) int { return a.ID.Compare(b.ID) })

	for _, p := range ps {
		if p.Enacted || p.Version == nil || p.Expires+1 < ep || !sameRoot(p.Prev, v.forkRoot()) {
			continue
		}

		if forkPasses(v, p, ep) {
			v.ratifyFork(p, ep, u)
			return true
		}
	}
	return false
}

// forkPasses reports whether hard fork p's votes meet the committee's, the
// DReps' and the pools' thresholds (Ratify.lagda.md threshold:
// TriggerHardFork is CC, P4 and Q4). A pool that did not vote counts as no
// (AcceptedBySPO defaultVote), as poolsPass counts it. During the Conway
// bootstrap phase (major 9) the DReps' thresholds are zero
// (ConwayBootstrap.md), so their votes do not count; the devnet starts at
// 10, past it.
func forkPasses(v *View, p ProposalState, ep uint64) bool {
	t := CountVotes(v, p.ID)
	if !committeePasses(t) {
		return false
	}

	if v.VersionAt(ep).Major > bootstrapMajor && (t.DRepTotal == 0 || ratio(t.DRepYes, t.DRepTotal).Cmp(drepFork) < 0) {
		return false
	}
	return poolsPass(v, p.ID, poolFork)
}

// bootstrapMajor is the protocol version of the Conway bootstrap phase.
const bootstrapMajor = 9

// committeePasses reports that the committee's yes seats are two thirds of
// those that did not abstain, with at least one counted.
func committeePasses(t Tally) bool {
	counted := t.CommitteeSeats - t.CommitteeAbstain
	return counted > 0 && big.NewRat(int64(t.CommitteeYes), int64(counted)).Cmp(committeeQuorum) >= 0
}

// passes reports whether p's votes meet every threshold that applies: the
// committee's two thirds of the seats that did not abstain, the DRep
// threshold of its groups, and for a security-group change the pools'
// (Ratify.hs committeeAcceptedRatio, dRepAcceptedRatio and
// spoAcceptedRatio). No DRep stake ratifies nothing.
func passes(v *View, p ProposalState) bool {
	t := CountVotes(v, p.ID)
	if !committeePasses(t) || t.DRepTotal == 0 {
		return false
	}

	need := drepOther
	if p.GovGroup {
		need = drepGov
	}

	if ratio(t.DRepYes, t.DRepTotal).Cmp(need) < 0 {
		return false
	}
	return !p.Pools || poolsPass(v, p.ID, poolSecurity)
}

// poolsPass weighs each pool by its stake in the set snapshot, yes over
// yes, no and unvoted, against threshold th.
func poolsPass(v *View, id GovID, th *big.Rat) bool {
	var yes, total uint64
	for pool, n := range v.ep.set.ByPool() {
		switch c, ok := v.votes[voteKey{id, Voter{Kind: common.VoterTypeStakingPoolKeyHash, Hash: pool}}]; {
		case ok && c == common.GovVoteAbstain:
		case ok && c == common.GovVoteYes:
			yes += n
			total += n
		default:
			total += n
		}
	}
	return total > 0 && ratio(yes, total).Cmp(th) >= 0
}

func ratio(a, b uint64) *big.Rat {
	return new(big.Rat).SetFrac(new(big.Int).SetUint64(a), new(big.Int).SetUint64(b))
}

func sameRoot(a, b *GovID) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}
