package chain

import (
	"bytes"
	"cmp"
	"encoding/hex"
	"maps"
	"slices"
	"strconv"

	"github.com/blinklabs-io/gouroboros/ledger/common"

	"github.com/chrisguiney/cardano-continuum/internal/agenda"
	"github.com/chrisguiney/cardano-continuum/internal/population"
)

// GovID names a governance action: the transaction that proposed it and the
// action's index in that transaction.
type GovID struct {
	Tx    TxID
	Index uint32
}

// String is "txhash#index", the form agenda.ProposalID takes.
func (g GovID) String() string {
	return hex.EncodeToString(g.Tx[:]) + "#" + strconv.FormatUint(uint64(g.Index), 10)
}

func (g GovID) compare(o GovID) int {
	if c := bytes.Compare(g.Tx[:], o.Tx[:]); c != 0 {
		return c
	}
	return cmp.Compare(g.Index, o.Index)
}

// Voter is a governance voter. Kind follows the ledger's voter encoding
// (common.VoterType*).
type Voter struct {
	Kind uint8
	Hash common.Blake2b224
}

func (v Voter) compare(o Voter) int {
	if c := cmp.Compare(v.Kind, o.Kind); c != 0 {
		return c
	}
	return compareHash(v.Hash, o.Hash)
}

// Vote is one voting procedure; Choice is common.GovVote{No,Yes,Abstain}.
type Vote struct {
	Voter  Voter
	Action GovID
	Choice uint8
}

// Proposal is a governance action a transaction proposes. Type is the
// action type as profile files name it (build.ActionType), empty for one
// the view does not know; Pools reports that stake pools may vote on it.
type Proposal struct {
	ID      GovID
	Type    string
	Pools   bool
	Deposit uint64
}

// ProposalState is a tracked governance action: what it proposes and the
// last epoch it can be voted on.
type ProposalState struct {
	ID      GovID
	Expires uint64
	Type    string
	Pools   bool
	Deposit uint64
}

// proposal is a tracked governance action, without its ID.
type proposal struct {
	expires uint64
	typ     string
	pools   bool
	deposit uint64
}

// proposalOf reads a proposal procedure.
func proposalOf(id GovID, p common.ProposalProcedure) Proposal {
	typ, pools := actionOf(p.GovAction())
	return Proposal{ID: id, Type: typ, Pools: pools, Deposit: p.Deposit()}
}

// ActionTypes lists the action type names actionOf gives, sorted: the
// values a proposal's Type takes besides empty.
func ActionTypes() []string {
	return []string{"hard_fork_initiation", "info", "new_constitution", "no_confidence", "parameter_change", "treasury_withdrawals", "update_committee"}
}

// actionOf names a governance action's type and reports whether stake
// pools may vote on it. It is isStakePoolVotingAllowed of cardano-ledger
// (eras/conway/impl/src/Cardano/Ledger/Conway/Governance/Internal.hs lines
// 350-406 at 08773e9a8f911f67209560a4e401369cbb21a0cb, which the GOV rule
// applies as DisallowedVoters in Rules/Gov.hs lines 372-376): pools vote on
// no-confidence, committee updates, hard forks and info actions, and on a
// parameter change only when it modifies a security-group parameter; never
// on a treasury withdrawal or a new constitution. An action the view does
// not know is no type, and pools may not vote on it.
func actionOf(a common.GovAction) (string, bool) {
	switch x := a.(type) {
	case common.ParameterChangeGovAction:
		return "parameter_change", len(x.SecurityGroupFields()) > 0
	case *common.HardForkInitiationGovAction:
		return "hard_fork_initiation", true
	case *common.TreasuryWithdrawalGovAction:
		return "treasury_withdrawals", false
	case *common.NoConfidenceGovAction:
		return "no_confidence", true
	case *common.UpdateCommitteeGovAction:
		return "update_committee", true
	case *common.NewConstitutionGovAction:
		return "new_constitution", false
	case *common.InfoGovAction:
		return "info", true
	}
	return "", false
}

// VoteState is a tracked vote.
type VoteState struct {
	Action GovID
	Voter  Voter
	Choice uint8
}

type voteKey struct {
	action GovID
	voter  Voter
}

type govUndo struct {
	props map[GovID]proposal
	votes map[voteKey]uint8
}

// applyGov folds a transaction's proposals, then its votes. Every proposal
// is tracked; a vote only when the book names its voter (a DRep or a pool
// operator) and the proposal is tracked. Proposals are never ratified,
// enacted or removed by the view: they stop being open when their last
// epoch passes. It reports whether anything was tracked.
func (v *View) applyGov(tx Tx, u *undo) bool {
	seen := false
	for _, p := range tx.Proposals {
		v.touchGov(u)
		v.props[p.ID] = proposal{expires: v.cfg.Slots.EpochOfSlot(u.point.Slot) + v.cfg.GovLifetime, typ: p.Type, pools: p.Pools, deposit: p.Deposit}
		seen = true
	}
	for _, x := range tx.Votes {
		if _, ok := v.props[x.Action]; !ok || !v.named(x.Voter) {
			continue
		}
		v.touchGov(u)
		v.votes[voteKey{x.Action, x.Voter}] = x.Choice
		seen = true
	}
	return seen
}

// named reports whether the book has the voter's credential.
func (v *View) named(x Voter) bool {
	switch x.Kind {
	case common.VoterTypeDRepKeyHash, common.VoterTypeDRepScriptHash:
		_, ok := v.cfg.Book.dreps[x.Hash]
		return ok
	case common.VoterTypeStakingPoolKeyHash:
		_, ok := v.cfg.Book.pools[x.Hash]
		return ok
	}
	return false
}

// touchGov saves the governance maps the first time a block changes them.
// The maps are small (a devnet's proposals and its own voters), so a copy
// per touched block is cheaper than a per-key log.
func (v *View) touchGov(u *undo) {
	if u.gov == nil {
		u.gov = &govUndo{props: maps.Clone(v.props), votes: maps.Clone(v.votes)}
	}
}

func (v *View) restoreGov(u undo) {
	if u.gov != nil {
		v.props, v.votes = u.gov.props, u.gov.votes
	}
}

func (v *View) govState() ([]ProposalState, []VoteState) {
	var ps []ProposalState
	for id, p := range v.props {
		ps = append(ps, ProposalState{ID: id, Expires: p.expires, Type: p.typ, Pools: p.pools, Deposit: p.deposit})
	}
	slices.SortFunc(ps, func(a, b ProposalState) int { return a.ID.compare(b.ID) })
	var vs []VoteState
	for k, c := range v.votes {
		vs = append(vs, VoteState{k.action, k.voter, c})
	}
	slices.SortFunc(vs, func(a, b VoteState) int {
		if c := a.Action.compare(b.Action); c != 0 {
			return c
		}
		return a.Voter.compare(b.Voter)
	})
	return ps, vs
}

// The methods below implement agenda.Governance.

// OpenProposals lists the tracked governance actions whose last epoch has
// not passed, by ID.
func (v *View) OpenProposals() []agenda.ProposalInfo {
	ep := v.Epoch()
	ps, _ := v.govState()
	var out []agenda.ProposalInfo
	for _, p := range ps {
		if p.Expires >= ep {
			out = append(out, agenda.ProposalInfo{ID: agenda.ProposalID(p.ID.String()), Expires: p.Expires, Type: p.Type, Pools: p.Pools})
		}
	}
	return out
}

// Voted reports that the actor, as DRep or pool operator, has a vote on p.
func (v *View) Voted(actor population.ActorID, p agenda.ProposalID) bool {
	b := v.cfg.Book
	for k := range v.votes {
		if agenda.ProposalID(k.action.String()) != p {
			continue
		}
		switch k.voter.Kind {
		case common.VoterTypeDRepKeyHash, common.VoterTypeDRepScriptHash:
			if b.drepOf[actor] == k.voter.Hash && b.dreps[k.voter.Hash] == actor {
				return true
			}
		case common.VoterTypeStakingPoolKeyHash:
			if b.poolOf[actor] == k.voter.Hash && b.pools[k.voter.Hash] == actor {
				return true
			}
		}
	}
	return false
}

// DReps lists the registered DRep actors, by ID.
func (v *View) DReps() []population.ActorID {
	var out []population.ActorID
	for cred := range v.dreps {
		out = append(out, v.cfg.Book.dreps[cred])
	}
	slices.Sort(out)
	return out
}

// VoteDelegated reports that the actor's stake credential has a vote
// delegation.
func (v *View) VoteDelegated(actor population.ActorID) bool {
	cred, ok := v.cfg.Book.stakeOf[actor]
	return ok && v.stake[cred].Votes
}

// VotingPower is the lovelace behind a voter's vote now: for a DRep the
// stake of every credential that delegates its vote to it, for a pool the
// stake delegated to the pool. A committee member's vote is one vote, not
// stake, so it has none, as has a voter the view does not track. Only the
// outputs the book names count, so this is the power the run's own
// actors give.
func (v *View) VotingPower(x Voter) uint64 {
	switch x.Kind {
	case common.VoterTypeDRepKeyHash, common.VoterTypeDRepScriptHash:
		stake := v.credStake()
		var sum uint64
		for cred, s := range v.stake {
			if s.Registered && s.Votes && s.DRep.Kind == drepKindOf(x.Kind) && s.DRep.Cred == x.Hash {
				sum += stake[cred]
			}
		}
		return sum
	case common.VoterTypeStakingPoolKeyHash:
		per, _ := v.poolStake()
		return per[x.Hash]
	}
	return 0
}

// drepKindOf is the drep encoding (common.DrepType*) of a DRep voter kind.
func drepKindOf(voter uint8) int {
	if voter == common.VoterTypeDRepScriptHash {
		return common.DrepTypeScriptHash
	}
	return common.DrepTypeAddrKeyHash
}
