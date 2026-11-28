package chain

import (
	"bytes"
	"cmp"
	"encoding/hex"
	"maps"
	"slices"
	"strconv"

	"github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/gouroboros/ledger/conway"

	"github.com/chrisguiney/cardano-continuum/internal/agenda"
	"github.com/chrisguiney/cardano-continuum/internal/population"
	"github.com/chrisguiney/cardano-continuum/internal/protocol"
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

// Compare orders actions by transaction hash, then index.
func (g GovID) Compare(o GovID) int {
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
	// Prev is a parameter change's or a hard fork's predecessor
	// (prevGovActionId), nil for none.
	Prev *GovID
	// GovGroup reports that a parameter change touches the governance
	// group, whose DRep threshold is the highest (govGroup).
	GovGroup bool
	// Version is the protocol version a hard fork moves to, nil for any
	// other action.
	Version *protocol.Version
}

// ProposalState is a tracked governance action: what it proposes, the
// last epoch it can be voted on, and whether the view's ratification
// (ratify.go) enacted it.
type ProposalState struct {
	ID      GovID
	Expires uint64
	Type    string
	Pools   bool
	Deposit uint64
	// Prev, GovGroup and Version are the Proposal's.
	Prev     *GovID
	GovGroup bool
	Version  *protocol.Version
	// Enacted is set at the epoch boundary that ratified the action: a
	// parameter change takes effect there, a hard fork at the next
	// (Fork.At).
	Enacted bool
}

// proposal is a tracked governance action, without its ID.
type proposal struct {
	// expires is the last epoch the action can be voted on.
	expires uint64
	// typ and pools are the Proposal's Type and Pools.
	typ   string
	pools bool
	// deposit is the lovelace the proposer deposited.
	deposit uint64
	// prev, govGroup and version are the Proposal's Prev, GovGroup and
	// Version.
	prev     *GovID
	govGroup bool
	version  *protocol.Version
	// enacted is set by ratify.
	enacted bool
}

// proposalOf reads a proposal procedure.
func proposalOf(id GovID, p common.ProposalProcedure) Proposal {
	typ, pools := actionOf(p.GovAction())
	prev, gov := changeOf(p.GovAction())
	if fp, v := forkOf(p.GovAction()); v != nil {
		return Proposal{
			ID:      id,
			Type:    typ,
			Pools:   pools,
			Deposit: p.Deposit(),
			Prev:    fp,
			Version: v,
		}
	}
	return Proposal{
		ID:       id,
		Type:     typ,
		Pools:    pools,
		Deposit:  p.Deposit(),
		Prev:     prev,
		GovGroup: gov,
	}
}

// changeOf reads a parameter change's predecessor and whether it touches
// the governance group; any other action has neither.
func changeOf(a common.GovAction) (*GovID, bool) {
	pc, ok := a.(*conway.ConwayParameterChangeGovAction)
	if !ok {
		return nil, false
	}

	var prev *GovID
	if id := pc.PreviousGovActionId(); id != nil {
		prev = &GovID{Tx: id.TransactionId, Index: id.GovActionIdx}
	}
	return prev, govGroup(pc.ParamUpdate)
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

// MayVote reports which voter classes the ledger lets vote on an action of
// type typ (an ActionTypes name); pools is Proposal.Pools, which for a
// parameter change says it touches the security group. DReps may vote on
// every action; the committee on all but no-confidence and committee
// updates, which would let it vote on its own removal; pools as actionOf
// says. The class sets are isDRepVotingAllowed, isCommitteeVotingAllowed and
// isStakePoolVotingAllowed of cardano-ledger Governance/Internal.hs. An
// unknown type has no voters.
func MayVote(typ string, pools bool) (drep, spo, committee bool) {
	if typ == "" {
		return false, false, false
	}
	return true, pools, typ != "no_confidence" && typ != "update_committee"
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
	root  *GovID
	forks []Fork
}

// gov is the view's governance state: every proposal seen, the votes of
// the voters the book names on them, the parameter-change root and the
// hard forks ratified.
type gov struct {
	// book names the run's voters.
	book *Book
	// props is every proposal seen, enacted or not.
	props map[GovID]proposal
	// votes is the book's voters' votes on props.
	votes map[voteKey]uint8
	// root is the last parameter change ratify enacted, nil while none
	// has been: the predecessor the ledger accepts for the next one.
	root *GovID
	// forks is the protocol-version history; its last fork is the
	// hard-fork root.
	forks
}

func newGov(b *Book, base protocol.Version) gov {
	return gov{book: b, props: map[GovID]proposal{}, votes: map[voteKey]uint8{}, forks: forks{base: base}}
}

func (g gov) clone() gov {
	g.props = maps.Clone(g.props)
	g.votes = maps.Clone(g.votes)
	g.list = slices.Clone(g.list)
	return g
}

// applyGov folds a transaction's proposals, then its votes. Every proposal
// is tracked; a vote only when the book names its voter (a DRep, a pool
// operator or a committee seat) and the proposal is tracked. A proposal
// stops being open when its last epoch passes or when the epoch
// transition enacts it (ratify); it is never removed, so the proposer can
// replay its whole chain. It reports whether anything was tracked.
func (g *gov) applyGov(tx Tx, expires uint64, u *undo) bool {
	seen := false
	for _, p := range tx.Proposals {
		g.touchGov(u)
		g.props[p.ID] = proposal{
			expires:  expires,
			typ:      p.Type,
			pools:    p.Pools,
			deposit:  p.Deposit,
			prev:     p.Prev,
			govGroup: p.GovGroup,
			version:  p.Version,
		}
		seen = true
	}
	for _, x := range tx.Votes {
		if _, ok := g.props[x.Action]; !ok || !g.named(x.Voter) {
			continue
		}
		g.touchGov(u)
		g.votes[voteKey{x.Action, x.Voter}] = x.Choice
		seen = true
	}
	return seen
}

// named reports whether the book has the voter's credential: a DRep, a
// pool or a committee hot key.
func (g *gov) named(x Voter) bool {
	switch x.Kind {
	case common.VoterTypeDRepKeyHash, common.VoterTypeDRepScriptHash:
		_, ok := g.book.dreps[x.Hash]
		return ok
	case common.VoterTypeStakingPoolKeyHash:
		_, ok := g.book.pools[x.Hash]
		return ok
	case common.VoterTypeConstitutionalCommitteeHotKeyHash:
		_, ok := g.book.committee[x.Hash]
		return ok
	}
	return false
}

// CommitteeVoted reports that committee seat i has a vote on p.
func (g *gov) CommitteeVoted(i int, p agenda.ProposalID) bool {
	hot, ok := g.book.seats[i]
	if !ok {
		return false
	}
	for k := range g.votes {
		if k.voter.Kind == common.VoterTypeConstitutionalCommitteeHotKeyHash && k.voter.Hash == hot && agenda.ProposalID(k.action.String()) == p {
			return true
		}
	}
	return false
}

// touchGov saves the governance maps the first time a block changes them.
// The maps are small (a devnet's proposals and its own voters), so a copy
// per touched block is cheaper than a per-key log.
func (g *gov) touchGov(u *undo) {
	if u.gov == nil {
		u.gov = &govUndo{props: maps.Clone(g.props), votes: maps.Clone(g.votes), root: g.root, forks: slices.Clone(g.list)}
	}
}

func (g *gov) restoreGov(u undo) {
	if u.gov != nil {
		g.props, g.votes, g.root, g.list = u.gov.props, u.gov.votes, u.gov.root, u.gov.forks
	}
}

// enact marks the parameter change id enacted and makes it the root.
func (g *gov) enact(id GovID, u *undo) {
	g.touchGov(u)
	p := g.props[id]
	p.enacted = true
	g.props[id] = p
	g.root = &id
}

// ratifyFork marks the hard fork p ratified at the boundary into epoch
// ep: it is the hard-fork root, and its version is in force from ep + 1.
func (g *gov) ratifyFork(p ProposalState, ep uint64, u *undo) {
	g.touchGov(u)
	x := g.props[p.ID]
	x.enacted = true
	g.props[p.ID] = x
	g.list = append(g.list, Fork{ID: p.ID, Version: *p.Version, At: ep + 1})
}

// restoreRoot sets the root from the enacted parameter changes after a
// restart: each enacted change names the root as it stood, so they form
// one chain, and the root is the one no other enacted change names. Hard
// forks are ratified too, but have their own chain (forks).
func (g *gov) restoreRoot() {
	named := map[GovID]bool{}
	for _, p := range g.props {
		if p.enacted && p.typ == "parameter_change" && p.prev != nil {
			named[*p.prev] = true
		}
	}

	for id, p := range g.props {
		if p.enacted && p.typ == "parameter_change" && !named[id] {
			g.root = &id
		}
	}
}

func (g *gov) govState() ([]ProposalState, []VoteState) {
	var ps []ProposalState
	for id, p := range g.props {
		ps = append(ps, ProposalState{
			ID:       id,
			Expires:  p.expires,
			Type:     p.typ,
			Pools:    p.pools,
			Deposit:  p.deposit,
			Prev:     p.prev,
			GovGroup: p.govGroup,
			Version:  p.version,
			Enacted:  p.enacted,
		})
	}
	slices.SortFunc(ps, func(a, b ProposalState) int { return a.ID.Compare(b.ID) })
	var vs []VoteState
	for k, c := range g.votes {
		vs = append(vs, VoteState{k.action, k.voter, c})
	}
	slices.SortFunc(vs, func(a, b VoteState) int {
		if c := a.Action.Compare(b.Action); c != 0 {
			return c
		}
		return a.Voter.compare(b.Voter)
	})
	return ps, vs
}

// Proposals lists the tracked governance actions of type typ, open or not,
// by last voting epoch, then ID: the order they were proposed in.
func (g *gov) Proposals(typ string) []ProposalState {
	ps, _ := g.govState()
	var out []ProposalState
	for _, p := range ps {
		if p.Type == typ {
			out = append(out, p)
		}
	}
	slices.SortStableFunc(out, func(a, b ProposalState) int { return cmp.Compare(a.Expires, b.Expires) })
	return out
}

// open lists the tracked governance actions whose last epoch is not
// before ep and that are not enacted, by ID. The ledger removes an enacted
// action, so a vote on one is refused (GovActionsDoNotExist).
func (g *gov) open(ep uint64) []agenda.ProposalInfo {
	ps, _ := g.govState()
	var out []agenda.ProposalInfo
	for _, p := range ps {
		if p.Expires >= ep && !p.Enacted {
			out = append(out, agenda.ProposalInfo{ID: agenda.ProposalID(p.ID.String()), Expires: p.Expires, Type: p.Type, Pools: p.Pools})
		}
	}
	return out
}

// Voted reports that the actor, as DRep or pool operator, has a vote on p.
func (g *gov) Voted(actor population.ActorID, p agenda.ProposalID) bool {
	b := g.book
	for k := range g.votes {
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

// drepKindOf is the drep encoding (common.DrepType*) of a DRep voter kind.
func drepKindOf(voter uint8) int {
	if voter == common.VoterTypeDRepScriptHash {
		return common.DrepTypeScriptHash
	}
	return common.DrepTypeAddrKeyHash
}
