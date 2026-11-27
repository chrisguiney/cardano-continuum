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
// parameter changes left them, and the root of the parameter-change purpose
// chain, the last one enacted (nil while none has been).
type enactment struct {
	// params are the protocol parameters with every enacted change applied.
	params conway.ConwayProtocolParameters
	// root is the last enacted parameter change.
	root *common.GovActionId
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

// ratify enacts in s, at the start of epoch ep, every open parameter change
// whose predecessor is the purpose root and whose DRep and committee votes
// (and pool votes, for a security-group change) meet the thresholds. It is
// written from cardano-ledger's Rules/Ratify.hs (dRepAcceptedRatio,
// committeeAcceptedRatio, spoAcceptedRatio) at 08773e9a, simplified: the
// fake ratifies and enacts at the same boundary, where the ledger enacts
// one epoch after ratifying, every DRep counts as active, and stake is the
// UTxO lovelace per credential in stake. Other action types are never
// enacted. Enacted proposals stay in the proposal map, so a later proposal
// can still name one as its predecessor.
func ratify(s *certState, ep uint64, stake map[common.Blake2b224]uint64) {
	ids := make([]common.GovActionId, 0, len(s.proposals))
	for id := range s.proposals {
		ids = append(ids, id)
	}
	slices.SortFunc(ids, compareAction)
	for _, id := range ids {
		p := s.proposals[id]
		pc, ok := p.state.Action.(*conway.ConwayParameterChangeGovAction)
		if !ok || p.enacted || p.expires+1 < ep || !sameAction(pc.ActionId, s.enact.root) {
			continue
		}
		if !accepted(s, id, pc, ep, stake) {
			continue
		}
		upd := pc.ParamUpdate
		if err := s.enact.params.ApplyUpdate(&upd); err != nil {
			continue
		}
		p.enacted = true
		s.proposals[id] = p
		root := id
		s.enact.root = &root
	}
}

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
