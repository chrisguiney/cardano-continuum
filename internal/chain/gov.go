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

// ProposalState is a tracked governance action: the last epoch it can be
// voted on.
type ProposalState struct {
	ID      GovID
	Expires uint64
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
	props map[GovID]uint64
	votes map[voteKey]uint8
}

// applyGov folds a transaction's proposals, then its votes. Every proposal
// is tracked; a vote only when the book names its voter (a DRep or a pool
// operator) and the proposal is tracked. Proposals are never ratified,
// enacted or removed by the view: they stop being open when their last
// epoch passes. It reports whether anything was tracked.
func (v *View) applyGov(tx Tx, u *undo) bool {
	seen := false
	for _, id := range tx.Proposals {
		v.touchGov(u)
		v.props[id] = v.cfg.Slots.EpochOfSlot(u.point.Slot) + v.cfg.GovLifetime
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
	for id, e := range v.props {
		ps = append(ps, ProposalState{id, e})
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
			out = append(out, agenda.ProposalInfo{ID: agenda.ProposalID(p.ID.String()), Expires: p.Expires})
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
