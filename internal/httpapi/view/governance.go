package view

import (
	"cmp"
	"encoding/hex"
	"slices"
	"strconv"

	"github.com/chrisguiney/cardano-continuum/internal/engine"
	"github.com/chrisguiney/cardano-continuum/internal/eventlog"
	"github.com/chrisguiney/cardano-continuum/internal/protocol"
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
	// Protocol is the protocol version in force at the tip, as the
	// engine's last Protocol decision reports it; nil before the first.
	Protocol *protocol.Version `json:"protocol_version,omitempty"`
	// Fork is a ratified hard fork waiting for the boundary that enacts
	// it; nil when none is.
	Fork *engine.Fork `json:"pending_fork,omitempty"`
	// Plan is the hard fork the run proposes by itself while its version is
	// not yet in force; nil without a schedule.
	Plan *protocol.Schedule `json:"scheduled_fork,omitempty"`
	// Treasury is the treasury as the engine's chain view reckons it; nil
	// before the engine's first Treasury decision.
	Treasury *TreasuryView `json:"treasury,omitempty"`
}

// TreasuryView is the treasury's lovelace now and its recent moves,
// oldest first: enacted withdrawals paid out at a boundary, or a rollback
// taking one back.
type TreasuryView struct {
	Now   uint64         `json:"now_lovelace"`
	Moves []TreasuryMove `json:"moves"`
}

// TreasuryMove is one Treasury decision that moved the treasury.
type TreasuryMove struct {
	Epoch  uint64 `json:"epoch"`
	Before uint64 `json:"before_lovelace"`
	After  uint64 `json:"after_lovelace"`
}

// maxTreasuryMoves bounds the moves the view keeps.
const maxTreasuryMoves = 100

// ProposalRow is one governance action. Status is open or expired, or
// unknown without a slot schedule, or ratified when the votes the run saw
// meet the thresholds (ratified). A hard fork is ratified or enacted as
// the engine's Protocol decisions report it.
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
	// Summary is one line on what the action asks for, set by the handler
	// from the explorer's decoding; the fold knows only the type.
	Summary string `json:"summary,omitempty"`
}

// ProposalDetail is /api/governance/{tx}/{index}'s fold side: the
// proposal's row with every current vote on it, newest first.
type ProposalDetail struct {
	// Treasury is the treasury now, which a withdrawal would draw on;
	// nil before the engine's first Treasury decision.
	Treasury *uint64     `json:"treasury_lovelace,omitempty"`
	Epoch    *uint64     `json:"epoch,omitempty"`
	Lifetime uint64      `json:"lifetime_epochs"`
	Row      ProposalRow `json:"proposal"`
	Votes    []VoteRow   `json:"votes"`
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
	Slot     uint64 `json:"slot"`
	At       int64  `json:"at"`
	Tx       string `json:"tx"`
	Proposal string `json:"proposal"`
	Type     string `json:"type"`
	Voter    string `json:"voter"`
	// VoterID is the voter's credential hash in hex.
	VoterID string  `json:"voter_id"`
	Actor   *uint64 `json:"actor,omitempty"`
	Profile string  `json:"profile,omitempty"`
	Choice  string  `json:"choice"`
	Power   uint64  `json:"power"`
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
	// proto is the engine's last Protocol decision, nil before the first.
	// The engine logs a new one for every change, rollbacks included, so
	// the last is the state at the tip.
	proto *engine.Protocol
	// enacted are the hard forks a Protocol decision showed enacted.
	enacted map[string]bool
	// treasury folds the Treasury decisions.
	treasury treasuryFold
}

// treasuryFold is the treasury after the last Treasury decision, nil
// before the first, and the decisions that moved it.
type treasuryFold struct {
	// now is the treasury after the last decision.
	now *uint64
	// moves are the decisions that moved it, oldest first.
	moves []TreasuryMove
}

// move folds a Treasury decision.
func (t *treasuryFold) move(r eventlog.Record) error {
	var tr engine.Treasury
	if err := eventlog.Decode(r.Body, &tr); err != nil {
		return err
	}

	now := tr.After
	t.now = &now
	if tr.Before != tr.After {
		t.moves = append(t.moves, TreasuryMove{Epoch: tr.Epoch, Before: tr.Before, After: tr.After})
		if len(t.moves) > maxTreasuryMoves {
			t.moves = t.moves[1:]
		}
	}
	return nil
}

// view is the folded treasury, nil before the first decision.
func (t *treasuryFold) view() *TreasuryView {
	if t.now == nil {
		return nil
	}
	return &TreasuryView{Now: *t.now, Moves: append([]TreasuryMove{}, t.moves...)}
}

// lovelace is the treasury now, nil before the first decision.
func (t *treasuryFold) lovelace() *uint64 {
	if t.now == nil {
		return nil
	}
	now := *t.now
	return &now
}

func newGovFold() *govFold {
	return &govFold{meta: map[engine.TxID]govMeta{}, props: map[string]*ProposalRow{}, votes: map[govVoteKey]VoteRow{}, enacted: map[string]bool{}}
}

// protocol folds a Protocol decision. A fork that was pending and whose
// version is now in force was enacted.
func (g *govFold) protocol(r eventlog.Record) error {
	var p engine.Protocol
	if err := eventlog.Decode(r.Body, &p); err != nil {
		return err
	}

	if old := g.proto; old != nil && old.Pending != nil && p.Pending == nil && p.Version == old.Pending.Version {
		g.enacted[old.Pending.Proposal] = true
	}
	g.proto = &p
	return nil
}

// forkStatus is a hard fork's status from the Protocol decisions, empty
// when they say nothing of it.
func (g *govFold) forkStatus(id string) string {
	switch {
	case g.enacted[id]:
		return "enacted"
	case g.proto != nil && g.proto.Pending != nil && g.proto.Pending.Proposal == id:
		return "ratified"
	}
	return ""
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
	row := VoteRow{Slot: slot, At: at, Tx: hex.EncodeToString(tx[:]), Proposal: id, Voter: voterClass(v.Kind), VoterID: hex.EncodeToString(v.Hash[:]), Choice: choiceNames[v.Choice]}
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

// enactable are the action types the run proposes that a devnet can
// enact: the network proposer's parameter changes and treasury
// withdrawals. Info actions never enact, and honest voters contest the
// rest.
var enactable = map[string]bool{"parameter_change": true, "treasury_withdrawals": true}

// ratified reports that p's votes meet the devnet genesis thresholds
// (internal/genesis renderConway): two thirds of the committee votes cast
// and at least two seats, 67% of the DRep power that voted yes or no, and
// for a security-group parameter change 51% of the pool power likewise.
// It is an estimate from the votes alone: the ledger also counts the stake
// of DReps and pools that did not vote, as no, which the run's log does
// not carry, and enacts at the epoch boundary after ratifying.
func ratified(p ProposalRow) bool {
	if !enactable[p.Type] {
		return false
	}
	cc, d := p.Tally[classCommittee], p.Tally[classDRep]
	if cc.Yes < 2 || 3*cc.Yes < 2*(cc.Yes+cc.No) || !share(d, 67) {
		return false
	}
	return !p.PoolsMayVote || share(p.Tally[classSPO], 51)
}

// share reports that yes power is at least pct percent of yes and no power,
// and positive.
func share(t Tally, pct uint64) bool {
	return t.YesPower > 0 && 100*t.YesPower >= pct*(t.YesPower+t.NoPower)
}

// view builds the GovernanceView at tip. profileOf names an actor's
// profile; slots may be nil.
func (g *govFold) view(slots *sched.Slots, tip uint64, life uint64, profileOf func(uint64) string) GovernanceView {
	out := GovernanceView{Lifetime: life, Proposals: []ProposalRow{}, Votes: []VoteRow{}, Pools: []PoolVotes{}, Treasury: g.treasury.view()}
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

	for i := range out.Proposals {
		if out.Proposals[i].Status != "unknown" && ratified(out.Proposals[i]) {
			out.Proposals[i].Status = "ratified"
		}

		if st := g.forkStatus(out.Proposals[i].ID); st != "" {
			out.Proposals[i].Status = st
		}
	}

	if g.proto != nil {
		v := g.proto.Version
		out.Protocol, out.Fork, out.Plan = &v, g.proto.Pending, g.proto.Plan
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

// detail is one proposal's row and its current votes, newest first; ok is
// false when the proposal is not on the chain.
func (g *govFold) detail(id string, slots *sched.Slots, tip uint64, life uint64, profileOf func(uint64) string) (ProposalDetail, bool) {
	v := g.view(slots, tip, life, profileOf)
	i := slices.IndexFunc(v.Proposals, func(p ProposalRow) bool { return p.ID == id })
	if i < 0 {
		return ProposalDetail{}, false
	}
	d := ProposalDetail{Epoch: v.Epoch, Lifetime: life, Row: v.Proposals[i], Votes: []VoteRow{}, Treasury: g.treasury.lovelace()}
	for _, r := range g.sortedVotes() {
		if r.Proposal != id {
			continue
		}
		if r.Actor != nil {
			r.Profile = profileOf(*r.Actor)
		}
		d.Votes = append(d.Votes, r)
	}
	slices.Reverse(d.Votes)
	return d, true
}
