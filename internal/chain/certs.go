package chain

import (
	"maps"
	"slices"

	"github.com/blinklabs-io/gouroboros/ledger/common"

	"github.com/chrisguiney/cardano-continuum/internal/policy"
	"github.com/chrisguiney/cardano-continuum/internal/population"
)

// accounts is the view's stake credentials and DReps: registrations,
// pool delegations and vote delegations of the credentials the book names.
type accounts struct {
	book  *Book
	stake map[common.Blake2b224]StakeState
	dreps map[common.Blake2b224]bool
}

func newAccounts(b *Book) accounts {
	return accounts{book: b, stake: map[common.Blake2b224]StakeState{}, dreps: map[common.Blake2b224]bool{}}
}

func (a accounts) clone() accounts {
	a.stake = maps.Clone(a.stake)
	a.dreps = maps.Clone(a.dreps)
	return a
}

// applyCert folds one certificate of a valid transaction and reports
// whether it touched anything the book names. The cases follow the Conway
// DELEG, POOL and GOVCERT rules in cardano-ledger
// (eras/conway/impl/src/Cardano/Ledger/Conway/Rules/Deleg.hs, Pool.hs and
// GovCert.hs) without their checks: the node has already validated the
// block.
func applyCert(a *accounts, r *registry, c common.Certificate, u *undo) bool {
	switch c := c.(type) {
	case *common.StakeRegistrationCertificate:
		return a.editStake(c.StakeCredential, u, register)
	case *common.RegistrationCertificate:
		return a.editStake(c.StakeCredential, u, register)
	case *common.StakeDeregistrationCertificate:
		return a.editStake(c.StakeCredential, u, deregister)
	case *common.DeregistrationCertificate:
		return a.editStake(c.StakeCredential, u, deregister)
	case *common.StakeDelegationCertificate:
		if c.StakeCredential == nil {
			return false
		}
		return a.editStake(*c.StakeCredential, u, delegate(c.PoolKeyHash))
	case *common.StakeRegistrationDelegationCertificate:
		return a.editStake(c.StakeCredential, u, register, delegate(c.PoolKeyHash))
	case *common.VoteDelegationCertificate:
		return a.editStake(c.StakeCredential, u, vote(c.Drep))
	case *common.StakeVoteDelegationCertificate:
		return a.editStake(c.StakeCredential, u, delegate(c.PoolKeyHash), vote(c.Drep))
	case *common.VoteRegistrationDelegationCertificate:
		return a.editStake(c.StakeCredential, u, register, vote(c.Drep))
	case *common.StakeVoteRegistrationDelegationCertificate:
		return a.editStake(c.StakeCredential, u, register, delegate(c.PoolKeyHash), vote(c.Drep))
	case *common.PoolRegistrationCertificate:
		return r.editPool(c.Operator, u, func(p *PoolState) {
			// Re-registering a pool cancels a pending retirement.
			p.Retiring, p.RetireAt, p.Retired = false, 0, false
		})
	case *common.PoolRetirementCertificate:
		return r.editPool(c.PoolKeyHash, u, func(p *PoolState) {
			p.Retiring, p.RetireAt = true, c.Epoch
		})
	case *common.RegistrationDrepCertificate:
		return a.editDRep(c.DrepCredential, u, true)
	case *common.DeregistrationDrepCertificate:
		return a.editDRep(c.DrepCredential, u, false)
	}
	return false
}

type stakeEdit func(*StakeState)

func register(s *StakeState) { s.Registered = true }

// deregister also drops both delegations: the ledger removes the account.
func deregister(s *StakeState) { *s = StakeState{Cred: s.Cred} }

func delegate(pool common.Blake2b224) stakeEdit {
	return func(s *StakeState) { s.Delegated, s.Pool = true, pool }
}

func vote(d common.Drep) stakeEdit {
	return func(s *StakeState) {
		s.Votes = true
		s.DRep = DRep{Kind: d.Type}
		copy(s.DRep.Cred[:], d.Credential)
	}
}

func (a *accounts) editStake(cred common.Credential, u *undo, edits ...stakeEdit) bool {
	k := cred.Credential
	if _, ok := a.book.stake[k]; !ok {
		return false
	}
	remember(&u.stake, a.stake, k)
	s := a.stake[k]
	s.Cred = k
	for _, e := range edits {
		e(&s)
	}
	// An unregistered account is no account: dropping it keeps a view that
	// saw a register and deregister equal to one that saw neither.
	if !s.Registered && !s.Delegated && !s.Votes {
		delete(a.stake, k)
		return true
	}
	a.stake[k] = s
	return true
}

// editDRep registers or deregisters a DRep. Deregistering also drops every
// vote delegation to it, as the GOVCERT rule does at protocol version 10 and
// later.
func (a *accounts) editDRep(cred common.Credential, u *undo, registered bool) bool {
	k := cred.Credential
	if _, ok := a.book.dreps[k]; !ok {
		return false
	}
	remember(&u.dreps, a.dreps, k)
	if registered {
		a.dreps[k] = true
		return true
	}
	delete(a.dreps, k)
	for _, sc := range sortedKeys(a.stake) {
		s := a.stake[sc]
		if !s.Votes || s.DRep.Cred != k || s.DRep.Kind != int(cred.CredType) {
			continue
		}
		remember(&u.stake, a.stake, sc)
		s.Votes, s.DRep = false, DRep{}
		a.stake[sc] = s
	}
	return true
}

// StakeRegistered reports whether the actor's stake credential is registered.
func (a *accounts) StakeRegistered(id population.ActorID) bool {
	cred, ok := a.book.stakeOf[id]
	return ok && a.stake[cred].Registered
}

// DelegatedTo is the pool actor the actor's stake is delegated to. A
// delegation to a pool the book does not name reports false.
func (a *accounts) DelegatedTo(id population.ActorID) (population.ActorID, bool) {
	cred, ok := a.book.stakeOf[id]
	if !ok {
		return 0, false
	}
	s := a.stake[cred]
	if !s.Delegated {
		return 0, false
	}
	pool, ok := a.book.pools[s.Pool]
	return pool, ok
}

// DRepRegistered reports whether the actor's DRep credential is registered.
func (a *accounts) DRepRegistered(id population.ActorID) bool {
	cred, ok := a.book.drepOf[id]
	return ok && a.dreps[cred]
}

// StakeOf is the state of the actor's stake credential, or false when the
// book does not name one.
func (a *accounts) StakeOf(id population.ActorID) (StakeState, bool) {
	cred, ok := a.book.stakeOf[id]
	if !ok {
		return StakeState{}, false
	}
	s := a.stake[cred]
	s.Cred = cred
	return s, true
}

// DReps lists the registered DRep actors, by ID.
func (a *accounts) DReps() []population.ActorID {
	var out []population.ActorID
	for cred := range a.dreps {
		out = append(out, a.book.dreps[cred])
	}
	slices.Sort(out)
	return out
}

// VoteDelegated reports that the actor's stake credential has a vote
// delegation.
func (a *accounts) VoteDelegated(actor population.ActorID) bool {
	cred, ok := a.book.stakeOf[actor]
	return ok && a.stake[cred].Votes
}

// undelegate drops every pool delegation to pool, as POOLREAP does for a
// retired pool.
func (a *accounts) undelegate(pool common.Blake2b224, u *undo) {
	for _, cred := range sortedKeys(a.stake) {
		if s := a.stake[cred]; s.Delegated && s.Pool == pool {
			remember(&u.stake, a.stake, cred)
			s.Delegated, s.Pool = false, common.Blake2b224{}
			a.stake[cred] = s
		}
	}
}

// distribution is the stake of every registered, delegated credential;
// stake is the lovelace per credential (utxos.credStake).
func (a *accounts) distribution(stake map[common.Blake2b224]uint64) Snapshot {
	var out Snapshot
	for _, cred := range sortedKeys(a.stake) {
		s := a.stake[cred]
		if n := stake[cred]; s.Registered && s.Delegated && n > 0 {
			out = append(out, StakeEntry{Cred: cred, Pool: s.Pool, Stake: n})
		}
	}
	return out
}

// delegation is the confirmed delegation: every registered credential, with
// pool zero for one delegated nowhere the book names.
func (a *accounts) delegation(stake map[common.Blake2b224]uint64) policy.Schedule {
	var out policy.Schedule
	for _, cred := range sortedKeys(a.stake) {
		s := a.stake[cred]
		if !s.Registered {
			continue
		}
		d := policy.Delegation{Wallet: a.book.stake[cred], Stake: stake[cred]}
		if s.Delegated {
			d.Pool = a.book.pools[s.Pool]
		}
		out = append(out, d)
	}
	return out
}
