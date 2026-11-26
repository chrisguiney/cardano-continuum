package chain

import (
	"github.com/blinklabs-io/gouroboros/ledger/common"
)

// applyCert folds one certificate of a valid transaction and reports
// whether it touched anything the book names. The cases follow the Conway
// DELEG, POOL and GOVCERT rules in cardano-ledger
// (eras/conway/impl/src/Cardano/Ledger/Conway/Rules/Deleg.hs, Pool.hs and
// GovCert.hs) without their checks: the node has already validated the
// block.
func (v *View) applyCert(c common.Certificate, u *undo) bool {
	switch c := c.(type) {
	case *common.StakeRegistrationCertificate:
		return v.editStake(c.StakeCredential, u, register)
	case *common.RegistrationCertificate:
		return v.editStake(c.StakeCredential, u, register)
	case *common.StakeDeregistrationCertificate:
		return v.editStake(c.StakeCredential, u, deregister)
	case *common.DeregistrationCertificate:
		return v.editStake(c.StakeCredential, u, deregister)
	case *common.StakeDelegationCertificate:
		if c.StakeCredential == nil {
			return false
		}
		return v.editStake(*c.StakeCredential, u, delegate(c.PoolKeyHash))
	case *common.StakeRegistrationDelegationCertificate:
		return v.editStake(c.StakeCredential, u, register, delegate(c.PoolKeyHash))
	case *common.VoteDelegationCertificate:
		return v.editStake(c.StakeCredential, u, vote(c.Drep))
	case *common.StakeVoteDelegationCertificate:
		return v.editStake(c.StakeCredential, u, delegate(c.PoolKeyHash), vote(c.Drep))
	case *common.VoteRegistrationDelegationCertificate:
		return v.editStake(c.StakeCredential, u, register, vote(c.Drep))
	case *common.StakeVoteRegistrationDelegationCertificate:
		return v.editStake(c.StakeCredential, u, register, delegate(c.PoolKeyHash), vote(c.Drep))
	case *common.PoolRegistrationCertificate:
		return v.editPool(c.Operator, u, func(p *PoolState) {
			// Re-registering a pool cancels a pending retirement.
			p.Retiring, p.RetireAt, p.Retired = false, 0, false
		})
	case *common.PoolRetirementCertificate:
		return v.editPool(c.PoolKeyHash, u, func(p *PoolState) {
			p.Retiring, p.RetireAt = true, c.Epoch
		})
	case *common.RegistrationDrepCertificate:
		return v.editDRep(c.DrepCredential, u, true)
	case *common.DeregistrationDrepCertificate:
		return v.editDRep(c.DrepCredential, u, false)
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

func (v *View) editStake(cred common.Credential, u *undo, edits ...stakeEdit) bool {
	k := cred.Credential
	if _, ok := v.cfg.Book.stake[k]; !ok {
		return false
	}
	remember(&u.stake, v.stake, k)
	s := v.stake[k]
	s.Cred = k
	for _, e := range edits {
		e(&s)
	}
	// An unregistered account is no account: dropping it keeps a view that
	// saw a register and deregister equal to one that saw neither.
	if !s.Registered && !s.Delegated && !s.Votes {
		delete(v.stake, k)
		return true
	}
	v.stake[k] = s
	return true
}

func (v *View) editPool(id common.Blake2b224, u *undo, edit func(*PoolState)) bool {
	if _, ok := v.cfg.Book.pools[id]; !ok {
		return false
	}
	remember(&u.pools, v.pools, id)
	p := v.pools[id]
	p.ID = id
	edit(&p)
	v.pools[id] = p
	return true
}

// editDRep registers or deregisters a DRep. Deregistering also drops every
// vote delegation to it, as the GOVCERT rule does at protocol version 10 and
// later.
func (v *View) editDRep(cred common.Credential, u *undo, registered bool) bool {
	k := cred.Credential
	if _, ok := v.cfg.Book.dreps[k]; !ok {
		return false
	}
	remember(&u.dreps, v.dreps, k)
	if registered {
		v.dreps[k] = true
		return true
	}
	delete(v.dreps, k)
	for _, sc := range sortedKeys(v.stake) {
		s := v.stake[sc]
		if !s.Votes || s.DRep.Cred != k || s.DRep.Kind != int(cred.CredType) {
			continue
		}
		remember(&u.stake, v.stake, sc)
		s.Votes, s.DRep = false, DRep{}
		v.stake[sc] = s
	}
	return true
}
