package fakechain

import (
	"math/big"
	"slices"

	"github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/gouroboros/ledger/conway"
)

// committeeQuorum is the genesis committee's threshold, as internal/genesis
// renders it (renderConway: 2/3).
var committeeQuorum = big.NewRat(2, 3)

// enactment is what the fake has enacted: the protocol parameters as
// parameter changes and hard forks left them, and the root of each purpose
// chain the fake enacts, the last action of it ratified (nil while none
// has been).
type enactment struct {
	// params are the protocol parameters with every enacted change applied.
	params conway.ConwayProtocolParameters
	// root is the last enacted parameter change.
	root *common.GovActionId
	// hardFork is the last ratified hard fork.
	hardFork *common.GovActionId
	// fork is a ratified hard fork's version, waiting for the boundary
	// that enacts it; nil when none is. It is replaced, never mutated, so
	// a cloned state may share it.
	fork *pendingFork
}

// pendingFork is a ratified hard fork: the version it moves to and the
// epoch whose boundary enacts it, one after the boundary that ratified it.
type pendingFork struct {
	major, minor uint
	at           uint64
}

// tally is one voter class's yes and counted stake (or seats): Yes over
// Total is the ratio Ratify.hs compares with the threshold; abstentions are
// in neither.
type tally struct {
	yes, total uint64
}

// meets reports yes/total >= t; nothing counted never meets a positive
// threshold, as in the ledger.
func (t tally) meets(th *big.Rat) bool {
	if t.total == 0 {
		return th.Sign() <= 0
	}
	return new(big.Rat).SetFrac(new(big.Int).SetUint64(t.yes), new(big.Int).SetUint64(t.total)).Cmp(th) >= 0
}

// ratify enacts in s, at the start of epoch ep, the hard fork ratified at
// the boundary before, then ratifies the open proposals whose votes meet
// the thresholds and whose predecessor is their purpose's root, in the
// ledger's priority order (Gov.lagda.md govActionPriority: hard forks
// before parameter changes, then by ID). It is written from
// formal-ledger-specifications f8059737f1db,
// src/Ledger/Conway/Specification/Ratify.lagda.md (RATIFY, acceptConds,
// delayingAction) and cardano-ledger's Rules/Ratify.hs (dRepAcceptedRatio,
// committeeAcceptedRatio, spoAcceptedRatio) at 08773e9a, simplified:
//
//   - a parameter change is enacted at the boundary that ratifies it,
//     where the ledger enacts one epoch later; a hard fork keeps the
//     ledger's delay, since the protocol version decides which rules the
//     next epoch's blocks are held to (Enact.lagda.md, Enact-HF);
//   - a ratified hard fork is a delaying action: nothing after it is
//     ratified at the same boundary;
//   - every DRep counts as active, and stake is the UTxO lovelace per
//     credential in stake;
//   - other action types are never enacted.
//
// A ratified proposal stays in the proposal map until the next boundary
// (prune), and the root names it after that.
func ratify(s *certState, ep uint64, stake map[common.Blake2b224]uint64) {
	enactFork(s, ep)
	ids := make([]common.GovActionId, 0, len(s.proposals))
	for id := range s.proposals {
		ids = append(ids, id)
	}

	slices.SortFunc(ids, func(a, b common.GovActionId) int {
		if c := priority(s.proposals[a]) - priority(s.proposals[b]); c != 0 {
			return c
		}
		return compareAction(a, b)
	})
	for _, id := range ids {
		p := s.proposals[id]
		if p.enacted || p.expires+1 < ep {
			continue
		}

		switch a := p.state.Action.(type) {
		case *conway.ConwayParameterChangeGovAction:
			ratifyChange(s, id, a, ep, stake)
		case *common.HardForkInitiationGovAction:
			if ratifyFork(s, id, a, ep, stake) {
				return
			}
		case *common.TreasuryWithdrawalGovAction:
			ratifyWithdrawal(s, id, a, ep, stake)
		}
	}
}

// ratifyWithdrawal enacts the treasury withdrawal id when the committee
// and the DReps accept it (Ratify.lagda.md threshold: TreasuryWdrl is CC
// and P6; pools do not vote) and the treasury covers it with every
// withdrawal already enacted and not yet paid (Enact.lagda.md Enact-Wdrl:
// the sum of newWdrls is at most the treasury). The next boundary pays it
// (pay). The fake has no guardrail script, so the policy hash is not
// checked.
func ratifyWithdrawal(s *certState, id common.GovActionId, w *common.TreasuryWithdrawalGovAction, ep uint64, stake map[common.Blake2b224]uint64) {
	if !committeeTally(s, id, ep).meets(committeeQuorum) || !drepTally(s, id, stake).meets(s.enact.params.DRepVotingThresholds.TreasuryWithdrawal.Rat) {
		return
	}

	total := pending(s)
	for _, n := range w.Withdrawals {
		total += n
	}
	if total > s.treasury {
		return
	}

	if s.payout == nil {
		s.payout = map[credKey]uint64{}
	}
	for addr, n := range w.Withdrawals {
		s.payout[rewardKey(addr)] += n
	}
	p := s.proposals[id]
	p.enacted, p.enactedAt = true, ep
	s.proposals[id] = p
}

// priority is govActionPriority for the actions the fake ratifies; every
// other type sorts after them.
func priority(p proposal) int {
	switch p.state.Action.(type) {
	case *common.HardForkInitiationGovAction:
		return 3
	case *conway.ConwayParameterChangeGovAction:
		return 4
	case *common.TreasuryWithdrawalGovAction:
		return 5
	}
	return 6
}

// enactFork moves the protocol version to the pending hard fork's once its
// boundary has come.
func enactFork(s *certState, ep uint64) {
	f := s.enact.fork
	if f == nil || f.at > ep {
		return
	}
	s.enact.params.ProtocolVersion.Major, s.enact.params.ProtocolVersion.Minor = f.major, f.minor
	s.enact.fork = nil
}

// ratifyChange enacts the parameter change id when it names the root and
// its votes pass.
func ratifyChange(s *certState, id common.GovActionId, pc *conway.ConwayParameterChangeGovAction, ep uint64, stake map[common.Blake2b224]uint64) {
	if !sameAction(pc.ActionId, s.enact.root) || !accepted(s, id, pc, ep, stake) {
		return
	}

	upd := pc.ParamUpdate
	if err := s.enact.params.ApplyUpdate(&upd); err != nil {
		return
	}
	p := s.proposals[id]
	p.enacted, p.enactedAt = true, ep
	s.proposals[id] = p
	root := id
	s.enact.root = &root
}

// ratifyFork ratifies the hard fork id when it names the hard-fork root
// and the committee, the DReps and the pools accept it (Ratify.lagda.md
// threshold: CC, P4 and Q4), and reports whether it did. A pool that did
// not vote counts as no on a hard fork whatever its reward account
// delegates to (AcceptedBySPO defaultVote), which poolTally does for
// every action.
func ratifyFork(s *certState, id common.GovActionId, hf *common.HardForkInitiationGovAction, ep uint64, stake map[common.Blake2b224]uint64) bool {
	pp := &s.enact.params
	if !sameAction(hf.ActionId, s.enact.hardFork) || !committeeTally(s, id, ep).meets(committeeQuorum) {
		return false
	}

	if !drepTally(s, id, stake).meets(drepForkThreshold(pp)) || !poolTally(s, id).meets(pp.PoolVotingThresholds.HardForkInitiation.Rat) {
		return false
	}

	p := s.proposals[id]
	p.enacted, p.enactedAt = true, ep
	s.proposals[id] = p
	root := id
	s.enact.hardFork = &root
	s.enact.fork = &pendingFork{major: hf.ProtocolVersion.Major, minor: hf.ProtocolVersion.Minor, at: ep + 1}
	return true
}

// drepForkThreshold is P4, except during the Conway bootstrap phase
// (major 9), when DRep thresholds are zero and only the committee and the
// pools decide (formal-ledger-specifications ConwayBootstrap.md). The
// devnet starts at 10, past it.
func drepForkThreshold(pp *conway.ConwayProtocolParameters) *big.Rat {
	if pp.ProtocolVersion.Major < bootstrapEnd {
		return new(big.Rat)
	}
	return pp.DRepVotingThresholds.HardForkInitiation.Rat
}

// bootstrapEnd is the first major version after the Conway bootstrap
// phase.
const bootstrapEnd = 10

// accepted reports whether the change's votes meet every threshold that
// applies to it.
func accepted(s *certState, id common.GovActionId, pc *conway.ConwayParameterChangeGovAction, ep uint64, stake map[common.Blake2b224]uint64) bool {
	pp := &s.enact.params
	if !committeeTally(s, id, ep).meets(committeeQuorum) {
		return false
	}
	if !drepTally(s, id, stake).meets(drepThreshold(pc.ParamUpdate, pp.DRepVotingThresholds)) {
		return false
	}
	if len(pc.SecurityGroupFields()) == 0 {
		return true
	}
	return poolTally(s, id).meets(pp.PoolVotingThresholds.PpSecurityGroup.Rat)
}

// committeeTally counts the seats with an authorised hot key that have not
// expired or resigned: yes votes over yes, no and unvoted. Fewer such
// seats than minCommitteeSize count nothing, so the committee cannot
// ratify.
func committeeTally(s *certState, id common.GovActionId, ep uint64) tally {
	var t tally
	active := 0
	for _, k := range sortedCreds(s.committee) {
		m := s.committee[k]
		if m.hot == nil || m.resigned || m.expiry < ep {
			continue
		}
		active++
		switch v, ok := s.votes[voteKey{id, hotVoter(*m.hot), m.hot.hash}]; {
		case ok && v == common.GovVoteAbstain:
		case ok && v == common.GovVoteYes:
			t.yes++
			t.total++
		default:
			t.total++
		}
	}
	if uint(active) < s.enact.params.MinCommitteeSize {
		return tally{}
	}
	return t
}

func hotVoter(hot credKey) uint8 {
	if hot.kind == common.CredentialTypeScriptHash {
		return common.VoterTypeConstitutionalCommitteeHotScriptHash
	}
	return common.VoterTypeConstitutionalCommitteeHotKeyHash
}

// drepTally weighs each registered DRep by the stake delegated to it: yes
// over yes, no and unvoted. Stake delegated to always-abstain counts in
// neither; to always-no-confidence, as no.
func drepTally(s *certState, id common.GovActionId, stake map[common.Blake2b224]uint64) tally {
	var t tally
	for _, k := range sortedCreds(s.accounts) {
		a := s.accounts[k]
		if a.drep == nil || stake[k.hash] == 0 {
			continue
		}
		n := stake[k.hash]
		switch a.drep.Type {
		case common.DrepTypeAbstain:
			continue
		case common.DrepTypeNoConfidence:
			t.total += n
			continue
		}
		d := drepKey(*a.drep)
		if _, ok := s.dreps[d]; !ok {
			continue
		}
		voter := common.VoterTypeDRepKeyHash
		if d.kind == common.CredentialTypeScriptHash {
			voter = common.VoterTypeDRepScriptHash
		}
		switch v, ok := s.votes[voteKey{id, voter, d.hash}]; {
		case ok && v == common.GovVoteAbstain:
		case ok && v == common.GovVoteYes:
			t.yes += n
			t.total += n
		default:
			t.total += n
		}
	}
	return t
}

// poolTally weighs each pool by its stake in the set snapshot: yes over yes,
// no and unvoted.
func poolTally(s *certState, id common.GovActionId) tally {
	var t tally
	for _, sh := range s.epoch.set {
		switch v, ok := s.votes[voteKey{id, common.VoterTypeStakingPoolKeyHash, sh.Pool}]; {
		case ok && v == common.GovVoteAbstain:
		case ok && v == common.GovVoteYes:
			t.yes += sh.Stake
			t.total += sh.Stake
		default:
			t.total += sh.Stake
		}
	}
	return t
}

// drepThreshold is the highest DRep threshold of the parameter groups the
// update touches (Conway's PParamGroup, ParameterChange threshold rule).
// The groups of the parameters continuum proposes are listed; any other
// field takes the highest group threshold, which never ratifies what the
// ledger would not.
func drepThreshold(u conway.ConwayProtocolParameterUpdate, th conway.DRepVotingThresholds) *big.Rat {
	all := []*big.Rat{th.PpNetworkGroup.Rat, th.PpEconomicGroup.Rat, th.PpTechnicalGroup.Rat, th.PpGovGroup.Rat}
	known := (u.A0 != nil || u.Tau != nil || u.Rho != nil || u.DRepInactivityPeriod != nil || u.CommitteeTermLimit != nil) &&
		u.MaxTxSize == nil && u.MinFeeA == nil && u.MinFeeB == nil && u.KeyDeposit == nil && u.PoolDeposit == nil &&
		u.DRepDeposit == nil && u.GovActionDeposit == nil && u.GovActionValidityPeriod == nil && u.MinCommitteeSize == nil
	if !known {
		return slices.MaxFunc(all, func(a, b *big.Rat) int { return a.Cmp(b) })
	}
	var groups []*big.Rat
	if u.A0 != nil {
		groups = append(groups, th.PpTechnicalGroup.Rat)
	}
	if u.Tau != nil || u.Rho != nil {
		groups = append(groups, th.PpEconomicGroup.Rat)
	}
	if u.DRepInactivityPeriod != nil || u.CommitteeTermLimit != nil {
		groups = append(groups, th.PpGovGroup.Rat)
	}
	return slices.MaxFunc(groups, func(a, b *big.Rat) int { return a.Cmp(b) })
}

func sameAction(a, b *common.GovActionId) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Equal(*b)
}

func compareAction(a, b common.GovActionId) int {
	if c := slices.Compare(a.TransactionId[:], b.TransactionId[:]); c != 0 {
		return c
	}
	return int(a.GovActionIdx) - int(b.GovActionIdx)
}
