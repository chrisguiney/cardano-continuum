package fakechain

import (
	"maps"
	"slices"

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
	// refunded is the pool deposits returned at retirement. The ledger pays
	// them to the pool's reward account, which this fake never lets anyone
	// withdraw from, so they stay out of circulation and count as deposits.
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

// Ledger returns the state at the tip, in a fixed order.
func (c *Chain) Ledger() Ledger {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.certs
	l := Ledger{
		Label: s.epoch.label, Frozen: s.epoch.frozen,
		Mark: slices.Clone(s.epoch.mark), Set: slices.Clone(s.epoch.set),
	}
	for _, k := range sortedCreds(s.accounts) {
		a := s.accounts[k]
		acc := Account{Cred: k.hash}
		if a.pool != nil {
			p := common.Blake2b224(*a.pool)
			acc.Pool = &p
		}
		if a.drep != nil {
			d := *a.drep
			acc.DRep = &d
		}
		l.Accounts = append(l.Accounts, acc)
	}
	for id, p := range s.pools {
		l.Pools = append(l.Pools, PoolStatus{ID: common.Blake2b224(id), RetireAt: p.retire})
	}
	slices.SortFunc(l.Pools, func(a, b PoolStatus) int { return slices.Compare(a.ID[:], b.ID[:]) })
	for id := range s.epoch.retired {
		l.Retired = append(l.Retired, common.Blake2b224(id))
	}
	slices.SortFunc(l.Retired, func(a, b common.Blake2b224) int { return slices.Compare(a[:], b[:]) })
	for _, k := range sortedCreds(s.dreps) {
		l.DReps = append(l.DReps, k.hash)
	}
	for id, p := range s.proposals {
		l.Proposals = append(l.Proposals, ProposalStatus{ID: id, ExpirySlot: p.state.ExpirySlot})
	}
	slices.SortFunc(l.Proposals, func(a, b ProposalStatus) int {
		if c := slices.Compare(a.ID.TransactionId[:], b.ID.TransactionId[:]); c != 0 {
			return c
		}
		return int(a.ID.GovActionIdx) - int(b.ID.GovActionIdx)
	})
	for k, v := range s.votes {
		l.Votes = append(l.Votes, Vote{Action: k.action, Kind: k.kind, Hash: k.hash, Choice: v})
	}
	slices.SortFunc(l.Votes, func(a, b Vote) int {
		if c := slices.Compare(a.Action.TransactionId[:], b.Action.TransactionId[:]); c != 0 {
			return c
		}
		if c := int(a.Action.GovActionIdx) - int(b.Action.GovActionIdx); c != 0 {
			return c
		}
		if c := int(a.Kind) - int(b.Kind); c != 0 {
			return c
		}
		return slices.Compare(a.Hash[:], b.Hash[:])
	})
	return l
}
