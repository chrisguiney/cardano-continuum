package fakechain

import (
	"bytes"
	"slices"

	"github.com/blinklabs-io/gouroboros/ledger/common"
	mockledger "github.com/blinklabs-io/ouroboros-mock/ledger"
)

// ledgerView is the common.LedgerState the gouroboros rules read. UTxO
// lookup, slot arithmetic and the static governance state (constitution,
// purpose roots) come from the embedded mock; delegation, pool, DRep,
// committee and proposal queries answer from the certState under
// validation, which the caller swaps in for each check.
//
// gouroboros finds optional capabilities (StakeCredentialDepositState,
// CommitteeVotingState, ...) by type assertion, so every method here must
// keep the exact signature of the interface it serves.
type ledgerView struct {
	*mockledger.MockLedgerState
	certs *certState
}

// settledPools is the ledger view as validation reads it, with the
// deferred pool parameters gouroboros' PV11 VRF-uniqueness rule asks for
// (common.FuturePoolParametersState). The fake applies a pool
// re-registration at once instead of at the next epoch boundary, so no
// parameters are ever pending and no VRF key is reserved by them.
type settledPools struct{ *ledgerView }

// IsFutureVrfKeyInUse implements common.FuturePoolParametersState.
func (settledPools) IsFutureVrfKeyInUse(common.Blake2b256) (bool, common.PoolKeyHash, error) {
	return false, common.PoolKeyHash{}, nil
}

func (v *ledgerView) account(c common.Credential) (*account, bool) {
	a, ok := v.certs.accounts[keyOf(c)]
	return a, ok
}

// IsStakeCredentialRegistered implements common.CertState.
func (v *ledgerView) IsStakeCredentialRegistered(c common.Credential) bool {
	_, ok := v.account(c)
	return ok
}

// StakeCredentialDeposit implements common.StakeCredentialDepositState.
func (v *ledgerView) StakeCredentialDeposit(c common.Credential) (*uint64, error) {
	a, ok := v.account(c)
	if !ok {
		return nil, nil
	}
	d := a.deposit
	return &d, nil
}

// IsRewardAccountRegistered implements common.RewardState.
func (v *ledgerView) IsRewardAccountRegistered(c common.Credential) bool {
	return v.IsStakeCredentialRegistered(c)
}

// RewardAccountBalance implements common.RewardState: zero for every
// registered account, since the fake pays no rewards.
func (v *ledgerView) RewardAccountBalance(c common.Credential) (*uint64, error) {
	if !v.IsStakeCredentialRegistered(c) {
		return nil, nil
	}
	var zero uint64
	return &zero, nil
}

// DRepDelegation implements common.DRepDelegationState.
func (v *ledgerView) DRepDelegation(c common.Credential) (*common.Drep, error) {
	a, ok := v.account(c)
	if !ok || a.drep == nil {
		return nil, nil
	}
	d := *a.drep
	return &d, nil
}

// PoolCurrentState implements common.PoolState.
func (v *ledgerView) PoolCurrentState(h common.PoolKeyHash) (*common.PoolRegistrationCertificate, *uint64, error) {
	p, ok := v.certs.pools[h]
	if !ok {
		return nil, nil, nil
	}
	reg := p.reg
	return &reg, p.retire, nil
}

// IsPoolRegistered implements common.PoolState.
func (v *ledgerView) IsPoolRegistered(h common.PoolKeyHash) bool {
	_, ok := v.certs.pools[h]
	return ok
}

// IsVrfKeyInUse implements common.PoolState.
func (v *ledgerView) IsVrfKeyInUse(vrf common.Blake2b256) (bool, common.PoolKeyHash, error) {
	ops := make([]common.PoolKeyHash, 0, len(v.certs.pools))
	for op := range v.certs.pools {
		ops = append(ops, op)
	}
	slices.SortFunc(ops, func(a, b common.PoolKeyHash) int { return bytes.Compare(a[:], b[:]) })
	for _, op := range ops {
		if v.certs.pools[op].reg.VrfKeyHash == vrf {
			return true, op, nil
		}
	}
	return false, common.PoolKeyHash{}, nil
}

// DRepRegistration implements common.GovState.
func (v *ledgerView) DRepRegistration(c common.Credential) (*common.DRepRegistration, error) {
	d, ok := v.certs.dreps[keyOf(c)]
	if !ok {
		return nil, nil
	}
	return &d, nil
}

// DRepRegistrations implements common.GovState.
func (v *ledgerView) DRepRegistrations() ([]common.DRepRegistration, error) {
	out := make([]common.DRepRegistration, 0, len(v.certs.dreps))
	for _, k := range sortedCreds(v.certs.dreps) {
		out = append(out, v.certs.dreps[k])
	}
	return out, nil
}

func (v *ledgerView) member(k credKey) *common.CommitteeMember {
	m, ok := v.certs.committee[k]
	if !ok {
		return nil
	}
	out := &common.CommitteeMember{ColdKey: k.hash, ExpiryEpoch: m.expiry, Resigned: m.resigned}
	if m.hot != nil {
		h := m.hot.hash
		out.HotKey = &h
	}
	return out
}

// CommitteeStateAvailable implements common.CommitteeCredentialState.
func (v *ledgerView) CommitteeStateAvailable() (bool, error) { return true, nil }

// CommitteeMember implements common.GovState for a key-hash cold credential.
func (v *ledgerView) CommitteeMember(cold common.Blake2b224) (*common.CommitteeMember, error) {
	return v.member(credKey{common.CredentialTypeAddrKeyHash, cold}), nil
}

// CommitteeCredentialMember implements common.CommitteeCredentialState.
func (v *ledgerView) CommitteeCredentialMember(cold common.Credential) (*common.CommitteeMember, error) {
	return v.member(keyOf(cold)), nil
}

// CommitteeHotCredentialMember implements common.CommitteeCredentialState:
// the first member, in credential order, whose hot key is hot.
func (v *ledgerView) CommitteeHotCredentialMember(hot common.Credential) (*common.CommitteeMember, error) {
	if ks := v.coldsFor(hot); len(ks) > 0 {
		return v.member(ks[0]), nil
	}
	return nil, nil
}

// CommitteeHotCredentialMembers implements common.CommitteeHotCredentialMembers.
func (v *ledgerView) CommitteeHotCredentialMembers(hot common.Credential) ([]*common.CommitteeMember, error) {
	var out []*common.CommitteeMember
	for _, k := range v.coldsFor(hot) {
		out = append(out, v.member(k))
	}
	return out, nil
}

// CommitteeHotCredentialColdCredentials implements common.CommitteeVotingState.
func (v *ledgerView) CommitteeHotCredentialColdCredentials(hot common.Credential) ([]common.Credential, error) {
	var out []common.Credential
	for _, k := range v.coldsFor(hot) {
		out = append(out, common.Credential{CredType: k.kind, Credential: k.hash})
	}
	return out, nil
}

// CommitteeCredentialIsElected implements common.CommitteeVotingState: every
// genesis seat is elected.
func (v *ledgerView) CommitteeCredentialIsElected(cold common.Credential) (bool, error) {
	_, ok := v.certs.committee[keyOf(cold)]
	return ok, nil
}

// CommitteeMembers implements common.GovState.
func (v *ledgerView) CommitteeMembers() ([]common.CommitteeMember, error) {
	var out []common.CommitteeMember
	for _, k := range sortedCreds(v.certs.committee) {
		out = append(out, *v.member(k))
	}
	return out, nil
}

func (v *ledgerView) coldsFor(hot common.Credential) []credKey {
	want := keyOf(hot)
	var out []credKey
	for _, k := range sortedCreds(v.certs.committee) {
		if m := v.certs.committee[k]; m.hot != nil && *m.hot == want {
			out = append(out, k)
		}
	}
	return out
}

// GovActionById implements common.GovState.
func (v *ledgerView) GovActionById(id common.GovActionId) (*common.GovActionState, error) {
	p, ok := v.certs.proposals[id]
	if !ok {
		return nil, nil
	}
	st := p.state
	return &st, nil
}

// GovActionExists implements common.GovState.
func (v *ledgerView) GovActionExists(id common.GovActionId) bool {
	_, ok := v.certs.proposals[id]
	return ok
}
