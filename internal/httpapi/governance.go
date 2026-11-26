package httpapi

import (
	"cmp"
	"encoding/hex"
	"slices"
	"strconv"

	"github.com/chrisguiney/cardano-continuum/internal/engine"
	"github.com/chrisguiney/cardano-continuum/internal/sched"
)

// The Governance view folds the chain's proposals and votes as the engine
// logged them: each BlockApplied input's digest says which transactions
// propose (with the action type, whether stake pools may vote, the
// deposit) and which vote (voter, action, choice), and the engine's own
// Submitted records name the actor behind a transaction and the voting
// power its chain view put behind the vote. A rollback takes the rolled
// back blocks' proposals and votes off again.

const (
	// maxRecentVotes bounds the vote feed the view keeps.
	maxRecentVotes = 200
	// maxPoolVotes is how many of each pool's votes the view lists.
	maxPoolVotes = 10
	// maxGovUndo bounds the blocks a rollback can take governance back
	// through. A node never rolls back past K blocks, and only blocks with
	// governance content are kept, so this is far beyond any devnet's K.
	maxGovUndo = 4096
)

// Voter classes, as Submitted names them.
const (
	classDRep      = "drep"
	classSPO       = "spo"
	classCommittee = "committee"
)

// GovernanceView is /api/governance: every proposal the chain carries with
// its votes tallied by voter class, the newest votes first, and each pool's
// recent SPO votes.
type GovernanceView struct {
	// Epoch is the current epoch and Lifetime the governance action
	// lifetime in epochs; Epoch is unset when the slot schedule is unknown.
	Epoch     *uint64       `json:"epoch,omitempty"`
	Lifetime  uint64        `json:"lifetime_epochs"`
	Proposals []ProposalRow `json:"proposals"`
	Votes     []VoteRow     `json:"votes"`
	Pools     []PoolVotes   `json:"pools"`
}

// ProposalRow is one governance action. Status is open or expired, or
// unknown without a slot schedule; the chain view does not ratify, so an
// enacted action shows as expired once its last epoch passes.
type ProposalRow struct {
	ID           string           `json:"id"`
	Type         string           `json:"type"`
	Proposer     *uint64          `json:"proposer,omitempty"`
	Profile      string           `json:"profile,omitempty"`
	Deposit      uint64           `json:"deposit_lovelace"`
	Slot         uint64           `json:"slot"`
	Epoch        *uint64          `json:"epoch,omitempty"`
	Expires      *uint64          `json:"expires_epoch,omitempty"`
	Status       string           `json:"status"`
	PoolsMayVote bool             `json:"spo_may_vote"`
	Tally        map[string]Tally `json:"tally"`
}

// Tally counts one voter class's current votes on a proposal, and the
// lovelace behind each choice where the engine's chain view knew it.
type Tally struct {
	Yes          int    `json:"yes"`
	No           int    `json:"no"`
	Abstain      int    `json:"abstain"`
	YesPower     uint64 `json:"yes_power"`
	NoPower      uint64 `json:"no_power"`
	AbstainPower uint64 `json:"abstain_power"`
}

// VoteRow is one vote on the chain. Actor and Power are known for the
// run's own voters only.
type VoteRow struct {
	Slot     uint64  `json:"slot"`
	At       int64   `json:"at"`
	Tx       string  `json:"tx"`
	Proposal string  `json:"proposal"`
	Type     string  `json:"type"`
	Voter    string  `json:"voter"`
	Actor    *uint64 `json:"actor,omitempty"`
	Profile  string  `json:"profile,omitempty"`
	Choice   string  `json:"choice"`
	Power    uint64  `json:"power"`
}

// PoolVotes is one pool actor's newest SPO votes.
type PoolVotes struct {
	Actor   uint64    `json:"actor"`
	Profile string    `json:"profile,omitempty"`
	Votes   []VoteRow `json:"votes"`
}

type govMeta struct {
	actor uint64
	power uint64
}

type govVoteKey struct {
	proposal string
	kind     uint8
	hash     [28]byte
}

type govVoteUndo struct {
	key  govVoteKey
	prev *VoteRow
}

type govBlockUndo struct {
	slot  uint64
	props []string
	votes []govVoteUndo
	// recent is how many votes the block added to the feed.
	recent int
}

// govFold is the governance read model. It is not safe for concurrent use:
// the Projection holds it under its lock.
type govFold struct {
	meta   map[engine.TxID]govMeta
	props  map[string]*ProposalRow
	order  []string
	votes  map[govVoteKey]VoteRow
	recent []VoteRow // oldest first
	undo   []govBlockUndo
}

func newGovFold() *govFold {
	return &govFold{meta: map[engine.TxID]govMeta{}, props: map[string]*ProposalRow{}, votes: map[govVoteKey]VoteRow{}}
}

// submitted notes the actor behind a governance transaction.
func (g *govFold) submitted(actor uint64, s engine.Submitted) {
	if s.Kind == "propose" || s.Kind == "vote" {
		g.meta[s.ID] = govMeta{actor: actor, power: s.Power}
	}
}

func proposalID(tx engine.TxID, i uint32) string {
	return hex.EncodeToString(tx[:]) + "#" + strconv.FormatUint(uint64(i), 10)
}

// block folds one block's proposals and votes.
func (g *govFold) block(at int64, slot uint64, c engine.BlockContent) {
	u := govBlockUndo{slot: slot}
	for _, tx := range c.Txs {
		for i := range tx.Proposals {
			u.props = append(u.props, g.propose(tx, i, slot))
		}
		for _, v := range tx.Votes {
			u.votes = append(u.votes, g.vote(at, slot, tx.ID, v))
			u.recent++
		}
	}
	if len(u.props) == 0 && len(u.votes) == 0 {
		return
	}
	if g.undo = append(g.undo, u); len(g.undo) > maxGovUndo {
		g.undo = slices.Delete(g.undo, 0, len(g.undo)-maxGovUndo)
	}
}

func (g *govFold) propose(tx engine.TxDigest, i uint32, slot uint64) string {
	id := proposalID(tx.ID, i)
	row := &ProposalRow{ID: id, Slot: slot}
	if int(i) < len(tx.Gov) {
		d := tx.Gov[i]
		row.Type, row.PoolsMayVote, row.Deposit = d.Type, d.Pools, d.Deposit
	}
	if m, ok := g.meta[tx.ID]; ok {
		a := m.actor
		row.Proposer = &a
	}
	g.props[id] = row
	g.order = append(g.order, id)
	return id
}

var choiceNames = map[uint8]string{0: "no", 1: "yes", 2: "abstain"}

// voterClass names a ledger voter kind (common.VoterType*).
func voterClass(kind uint8) string {
	switch kind {
	case 0, 1:
		return classCommittee
	case 2, 3:
		return classDRep
	}
	return classSPO
}

func (g *govFold) vote(at int64, slot uint64, tx engine.TxID, v engine.VoteDigest) govVoteUndo {
	id := proposalID(v.Tx, v.Index)
	key := govVoteKey{proposal: id, kind: v.Kind, hash: v.Hash}
	u := govVoteUndo{key: key}
	if prev, ok := g.votes[key]; ok {
		u.prev = &prev
	}
	row := VoteRow{Slot: slot, At: at, Tx: hex.EncodeToString(tx[:]), Proposal: id, Voter: voterClass(v.Kind), Choice: choiceNames[v.Choice]}
	if p := g.props[id]; p != nil {
		row.Type = p.Type
	}
	if m, ok := g.meta[tx]; ok {
		a := m.actor
		row.Actor, row.Power = &a, m.power
	}
	g.votes[key] = row
	if g.recent = append(g.recent, row); len(g.recent) > 2*maxRecentVotes {
		g.recent = slices.Delete(g.recent, 0, len(g.recent)-maxRecentVotes)
	}
	return u
}

// rollback takes off every block above slot.
func (g *govFold) rollback(slot uint64) {
	for len(g.undo) > 0 && g.undo[len(g.undo)-1].slot > slot {
		u := g.undo[len(g.undo)-1]
		g.undo = g.undo[:len(g.undo)-1]
		for i := len(u.votes) - 1; i >= 0; i-- {
			if v := u.votes[i]; v.prev != nil {
				g.votes[v.key] = *v.prev
			} else {
				delete(g.votes, v.key)
			}
		}
		for _, id := range u.props {
			delete(g.props, id)
			g.order = slices.DeleteFunc(g.order, func(o string) bool { return o == id })
		}
		g.recent = g.recent[:max(len(g.recent)-u.recent, 0)]
	}
}

// view builds the GovernanceView at tip. profileOf names an actor's
// profile; slots may be nil.
func (g *govFold) view(slots *sched.Slots, tip uint64, life uint64, profileOf func(uint64) string) GovernanceView {
	out := GovernanceView{Lifetime: life, Proposals: []ProposalRow{}, Votes: []VoteRow{}, Pools: []PoolVotes{}}
	var now uint64
	if slots != nil {
		now = slots.EpochOfSlot(tip)
		out.Epoch = &now
	}
	named := func(v VoteRow) VoteRow {
		if v.Actor != nil {
			v.Profile = profileOf(*v.Actor)
		}
		return v
	}
	for _, id := range g.order {
		p := *g.props[id]
		p.Status, p.Tally = "unknown", map[string]Tally{classDRep: {}, classSPO: {}, classCommittee: {}}
		if p.Proposer != nil {
			p.Profile = profileOf(*p.Proposer)
		}
		if slots != nil {
			e := slots.EpochOfSlot(p.Slot)
			x := e + life
			p.Epoch, p.Expires, p.Status = &e, &x, "open"
			if now > x {
				p.Status = "expired"
			}
		}
		out.Proposals = append(out.Proposals, p)
	}
	idx := map[string]int{}
	for i, p := range out.Proposals {
		idx[p.ID] = i
	}
	for _, v := range g.sortedVotes() {
		i, ok := idx[v.Proposal]
		if !ok {
			continue
		}
		t := out.Proposals[i].Tally[v.Voter]
		t.add(v.Choice, v.Power)
		out.Proposals[i].Tally[v.Voter] = t
	}
	pools := map[uint64]int{}
	for i := len(g.recent) - 1; i >= 0 && len(out.Votes) < maxRecentVotes; i-- {
		v := named(g.recent[i])
		out.Votes = append(out.Votes, v)
	}
	for i := len(g.recent) - 1; i >= 0; i-- {
		v := g.recent[i]
		if v.Voter != classSPO || v.Actor == nil {
			continue
		}
		j, ok := pools[*v.Actor]
		if !ok {
			j = len(out.Pools)
			pools[*v.Actor] = j
			out.Pools = append(out.Pools, PoolVotes{Actor: *v.Actor, Profile: profileOf(*v.Actor)})
		}
		if len(out.Pools[j].Votes) < maxPoolVotes {
			out.Pools[j].Votes = append(out.Pools[j].Votes, named(v))
		}
	}
	slices.SortFunc(out.Pools, func(a, b PoolVotes) int { return cmp.Compare(a.Actor, b.Actor) })
	return out
}

// sortedVotes are the current votes in a fixed order, so the tally's sums
// do not depend on map order.
func (g *govFold) sortedVotes() []VoteRow {
	out := make([]VoteRow, 0, len(g.votes))
	for _, v := range g.votes {
		out = append(out, v)
	}
	slices.SortFunc(out, func(a, b VoteRow) int {
		if c := cmp.Compare(a.Slot, b.Slot); c != 0 {
			return c
		}
		if c := cmp.Compare(a.Tx, b.Tx); c != 0 {
			return c
		}
		return cmp.Compare(a.Proposal, b.Proposal)
	})
	return out
}

func (t *Tally) add(choice string, power uint64) {
	switch choice {
	case "yes":
		t.Yes++
		t.YesPower += power
	case "no":
		t.No++
		t.NoPower += power
	case "abstain":
		t.Abstain++
		t.AbstainPower += power
	}
}
