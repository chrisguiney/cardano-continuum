package fakechain

import (
	"maps"

	"github.com/blinklabs-io/gouroboros/ledger/common"
)

// Share is one stake credential's entry in a stake snapshot: registered,
// delegated, with stake. Pool is the pool's key hash.
type Share struct {
	Cred  common.Blake2b224
	Pool  common.Blake2b224
	Stake uint64
}

// epochState is what the epoch transition owns: label is the epoch of the
// newest block, frozen is set when an epoch passes with no block (the
// ledger then never runs another transition), and mark and set are the
// stake snapshots of the last two transitions. retired remembers pools a
// transition removed, so a test can tell "retired" from "never registered".
type epochState struct {
	label     uint64
	frozen    bool
	mark, set []Share
	retired   map[common.PoolKeyHash]bool
	// refunded is the pool deposits returned at retirement and the
	// proposal deposits returned at removal. The ledger pays them to a
	// reward account, which this fake never lets anyone withdraw from, so
	// they stay out of circulation and count as deposits.
	refunded uint64
}

func (e epochState) clone() epochState {
	e.retired = maps.Clone(e.retired)
	return e
}

// newEpoch runs the transition the block of epoch ep triggers, before the
// block's transactions: the first block of an epoch rotates the snapshots
// (set takes mark, mark takes the delegation as it stands from stake, the
// lovelace per stake credential in the UTxO set), then retires the pools
// whose epoch has come and undelegates their wallets. The first block of a
// chain has no earlier epoch to close. This is written from the ledger's
// NEWEPOCH, SNAP and POOLREAP rules, independently of the engine's view, so
// the two can be compared.
func newEpoch(s *certState, ep uint64, first bool, stake map[common.Blake2b224]uint64) {
	e := &s.epoch
	if e.frozen || ep == e.label {
		return
	}
	switch {
	case first:
		e.label = ep
	case ep > e.label+1:
		e.frozen = true
	default:
		e.set, e.mark = e.mark, distribution(s, stake)
		e.label = ep
		reap(s, ep)
		prune(s, ep)
		ratify(s, ep, stake)
	}
}

func distribution(s *certState, stake map[common.Blake2b224]uint64) []Share {
	var out []Share
	for _, k := range sortedCreds(s.accounts) {
		a := s.accounts[k]
		if n := stake[k.hash]; a.pool != nil && n > 0 {
			out = append(out, Share{Cred: k.hash, Pool: *a.pool, Stake: n})
		}
	}
	return out
}

func reap(s *certState, ep uint64) {
	for id, p := range s.pools {
		if p.retire == nil || *p.retire > ep {
			continue
		}
		s.epoch.refunded += p.deposit
		delete(s.pools, id)
		if s.epoch.retired == nil {
			s.epoch.retired = map[common.PoolKeyHash]bool{}
		}
		s.epoch.retired[id] = true
		for _, a := range s.accounts {
			if a.pool != nil && *a.pool == id {
				a.pool = nil
			}
		}
	}
}

// prune removes, at the boundary into epoch ep, the parameter changes
// enacted at an earlier boundary, with their votes, and returns their
// deposits as reap does a pool's. The ledger ratifies from the snapshot
// taken at one boundary and enacts and removes at the next (Conway EPOCH
// rule, proposalsApplyEnactment); the fake enacts at the first, so it
// removes at the second, and a vote landing in between is accepted on
// both. The root stays: a revert naming it is still acceptable.
func prune(s *certState, ep uint64) {
	for id, p := range s.proposals {
		if !p.enacted || p.enactedAt >= ep {
			continue
		}
		s.epoch.refunded += p.deposit
		delete(s.proposals, id)
		for k := range s.votes {
			if k.action == id {
				delete(s.votes, k)
			}
		}
	}
}

// Account is a registered stake credential and what it is delegated to.
type Account struct {
	Cred common.Blake2b224
	Pool *common.Blake2b224
	DRep *common.Drep
}

// PoolStatus is a registered pool; RetireAt is its pending retirement epoch.
type PoolStatus struct {
	ID       common.Blake2b224
	RetireAt *uint64
}

// ProposalStatus is a governance action and its last slot for votes.
type ProposalStatus struct {
	ID         common.GovActionId
	ExpirySlot uint64
}

// Vote is one recorded vote: Kind and Hash are the voter as encoded in the
// voting procedure.
type Vote struct {
	Action common.GovActionId
	Kind   uint8
	Hash   common.Blake2b224
	Choice uint8
}

// Ledger is the delegation, pool and governance state at the tip, with the
// epoch snapshots. It is the ground truth tests compare a followed view to.
type Ledger struct {
	Label     uint64
	Frozen    bool
	Mark, Set []Share
	Accounts  []Account
	Pools     []PoolStatus
	// Retired lists the pools an epoch transition removed.
	Retired   []common.Blake2b224
	DReps     []common.Blake2b224
	Proposals []ProposalStatus
	Votes     []Vote
}
