package fakechain

import (
	"bytes"
	"cmp"
	"maps"
	"slices"

	"github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/gouroboros/ledger/conway"
)

// CommitteeSeat is one genesis constitutional committee member.
type CommitteeSeat struct {
	Cold   common.Blake2b224
	Expiry uint64
}

type credKey struct {
	kind uint
	hash common.Blake2b224
}

func keyOf(c common.Credential) credKey { return credKey{c.CredType, c.Credential} }

// rewardKey is the stake credential of a reward address.
func rewardKey(a *common.Address) credKey {
	kind := uint(common.CredentialTypeAddrKeyHash)
	if a.Type() == common.AddressTypeNoneScript {
		kind = common.CredentialTypeScriptHash
	}
	return credKey{kind, a.StakeKeyHash()}
}

type account struct {
	deposit uint64
	drep    *common.Drep
	pool    *common.PoolKeyHash
	// reward is the account's reward balance: what enacted treasury
	// withdrawals paid it, less what it withdrew.
	reward uint64
}

type pool struct {
	reg     common.PoolRegistrationCertificate
	retire  *uint64
	deposit uint64
}

type seat struct {
	expiry   uint64
	hot      *credKey
	resigned bool
}

// certState is the delegation and governance state the fake keeps beside the
// UTxO set: what the Conway CERTS and GOV rules read. It mirrors the effects
// gouroboros' BlockLedgerState folds within a block
// (ledger/common/block_state.go, applyCertificate and applyLevel), applied
// permanently.
//
// At an epoch boundary (epoch.go) the snapshots rotate, retiring pools
// retire, actions ratified at the boundary before are removed, a hard fork
// ratified then is enacted, and parameter changes and hard forks with the
// votes are ratified (ratify.go). Expired
// proposals are never removed, and no rewards accrue, so every reward
// balance is zero.
type certState struct {
	accounts  map[credKey]*account
	pools     map[common.PoolKeyHash]*pool
	dreps     map[credKey]common.DRepRegistration
	committee map[credKey]*seat
	proposals map[common.GovActionId]proposal
	votes     map[voteKey]uint8
	epoch     epochState
	enact     enactment
	// treasury is the treasury's lovelace: the configured start less the
	// enacted withdrawals paid out of it.
	treasury uint64
	// payout are the withdrawals enacted at the last boundary, by reward
	// account, which the next boundary pays (Epoch.lagda.md payout).
	payout map[credKey]uint64
}

type voteKey struct {
	action common.GovActionId
	kind   uint8
	hash   common.Blake2b224
}

type proposal struct {
	state   common.GovActionState
	deposit uint64
	// expires is the last epoch the proposal can be voted on.
	expires uint64
	// enacted is set when ratify applied the proposal.
	enacted bool
	// enactedAt is the epoch whose boundary enacted it; the boundary after
	// removes it (prune).
	enactedAt uint64
}

func newCertState(seats []CommitteeSeat, pp conway.ConwayProtocolParameters) *certState {
	s := &certState{
		enact:     enactment{params: pp},
		accounts:  map[credKey]*account{},
		pools:     map[common.PoolKeyHash]*pool{},
		dreps:     map[credKey]common.DRepRegistration{},
		committee: map[credKey]*seat{},
		proposals: map[common.GovActionId]proposal{},
		votes:     map[voteKey]uint8{},
	}
	for _, m := range seats {
		s.committee[credKey{common.CredentialTypeAddrKeyHash, m.Cold}] = &seat{expiry: m.Expiry}
	}
	return s
}

// stake registers the genesis pools and delegations.
func (s *certState) stake(gs []GenesisStake) {
	for _, g := range gs {
		op := g.Pool.Operator
		s.pools[op] = &pool{reg: g.Pool}
		for _, d := range g.Delegators {
			s.accounts[credKey{common.CredentialTypeAddrKeyHash, d}] = &account{pool: &op}
		}
	}
}

// clone returns a deep copy, so an overlay or an undo record can be mutated
// or restored independently.
func (s *certState) clone() *certState {
	out := &certState{
		accounts:  make(map[credKey]*account, len(s.accounts)),
		pools:     make(map[common.PoolKeyHash]*pool, len(s.pools)),
		dreps:     maps.Clone(s.dreps),
		committee: make(map[credKey]*seat, len(s.committee)),
		proposals: maps.Clone(s.proposals),
		votes:     maps.Clone(s.votes),
		epoch:     s.epoch.clone(),
		enact:     s.enact,
		treasury:  s.treasury,
		payout:    maps.Clone(s.payout),
	}
	for k, a := range s.accounts {
		c := *a
		out.accounts[k] = &c
	}
	for k, p := range s.pools {
		c := *p
		out.pools[k] = &c
	}
	for k, m := range s.committee {
		c := *m
		out.committee[k] = &c
	}
	return out
}

// deposits is the lovelace held in deposits: stake, pool, DRep and proposal,
// and pool deposits refunded to reward accounts.
func (s *certState) deposits() uint64 {
	var sum uint64
	for _, a := range s.accounts {
		sum += a.deposit
	}
	for _, p := range s.pools {
		sum += p.deposit
	}
	for _, d := range s.dreps {
		if d.Deposit != nil {
			sum += *d.Deposit
		}
	}
	for _, p := range s.proposals {
		sum += p.deposit
	}
	return sum + s.epoch.refunded
}

// held is the lovelace s holds outside the UTxO set, fees and deposits:
// the treasury and the reward balances. Withdrawals enacted but not yet
// paid are still in the treasury.
func held(s *certState) uint64 {
	sum := s.treasury
	for _, a := range s.accounts {
		sum += a.reward
	}
	return sum
}

// pending is the lovelace enacted withdrawals will pay at the next
// boundary.
func pending(s *certState) uint64 {
	var sum uint64
	for _, n := range s.payout {
		sum += n
	}
	return sum
}

// effects gives apply what it needs from the protocol parameters and clock.
type effects struct {
	pp *conway.ConwayProtocolParameters
	// expiry is the last slot a proposal made at the given slot can be voted
	// on.
	expiry func(slot uint64) uint64
	// epochOf is the epoch of a slot.
	epochOf func(slot uint64) uint64
}

// apply folds an accepted, phase-2-valid tx into s. Validation has already
// passed, so every certificate is applicable.
func (s *certState) apply(tx common.Transaction, slot uint64, r effects) {
	for addr, n := range tx.Withdrawals() {
		// The withdrawal rule held each amount to the whole balance.
		if a, ok := s.accounts[rewardKey(addr)]; ok && n != nil {
			a.reward -= min(a.reward, n.Uint64())
		}
	}
	for _, c := range tx.Certificates() {
		s.applyCert(c, r)
	}
	for i, p := range tx.ProposalProcedures() {
		id := common.GovActionId{TransactionId: tx.Hash(), GovActionIdx: uint32(i)}
		t, _ := actionType(p.GovAction())
		s.proposals[id] = proposal{
			state:   common.GovActionState{ActionId: id, ActionType: t, ExpirySlot: r.expiry(slot), Action: p.GovAction()},
			deposit: p.Deposit(),
			expires: r.epochOf(r.expiry(slot)),
		}
	}
	for voter, acts := range tx.VotingProcedures() {
		for id, vp := range acts {
			s.votes[voteKey{*id, voter.Type, voter.Hash}] = vp.Vote
		}
	}
}

func (s *certState) applyCert(cert common.Certificate, r effects) {
	switch c := cert.(type) {
	case *common.StakeRegistrationCertificate:
		s.accounts[keyOf(c.StakeCredential)] = &account{deposit: uint64(r.pp.KeyDeposit)}
	case *common.RegistrationCertificate:
		s.accounts[keyOf(c.StakeCredential)] = &account{deposit: c.Amount}
	case *common.StakeDelegationCertificate:
		s.delegatePool(*c.StakeCredential, c.PoolKeyHash)
	case *common.StakeRegistrationDelegationCertificate:
		s.accounts[keyOf(c.StakeCredential)] = &account{deposit: c.Amount, pool: &c.PoolKeyHash}
	case *common.VoteRegistrationDelegationCertificate:
		s.accounts[keyOf(c.StakeCredential)] = &account{deposit: c.Amount, drep: &c.Drep}
	case *common.StakeVoteRegistrationDelegationCertificate:
		s.accounts[keyOf(c.StakeCredential)] = &account{deposit: c.Amount, drep: &c.Drep, pool: &c.PoolKeyHash}
	case *common.StakeDeregistrationCertificate:
		delete(s.accounts, keyOf(c.StakeCredential))
	case *common.DeregistrationCertificate:
		delete(s.accounts, keyOf(c.StakeCredential))
	case *common.VoteDelegationCertificate:
		s.delegateVote(c.StakeCredential, c.Drep)
	case *common.StakeVoteDelegationCertificate:
		s.delegatePool(c.StakeCredential, c.PoolKeyHash)
		s.delegateVote(c.StakeCredential, c.Drep)
	case *common.PoolRegistrationCertificate:
		s.registerPool(c, r)
	case *common.PoolRetirementCertificate:
		if p, ok := s.pools[c.PoolKeyHash]; ok {
			e := c.Epoch
			p.retire = &e
		}
	default:
		s.applyGovCert(cert)
	}
}

func (s *certState) applyGovCert(cert common.Certificate) {
	switch c := cert.(type) {
	case *common.RegistrationDrepCertificate:
		d := c.Amount
		s.dreps[keyOf(c.DrepCredential)] = common.DRepRegistration{Credential: c.DrepCredential, Anchor: c.Anchor, Deposit: &d}
	case *common.UpdateDrepCertificate:
		if d, ok := s.dreps[keyOf(c.DrepCredential)]; ok {
			d.Anchor = c.Anchor
			s.dreps[keyOf(c.DrepCredential)] = d
		}
	case *common.DeregistrationDrepCertificate:
		k := keyOf(c.DrepCredential)
		delete(s.dreps, k)
		// The reference GOVCERT rule clears every delegation to a DRep that
		// deregisters.
		for _, a := range s.accounts {
			if a.drep != nil && drepKey(*a.drep) == k {
				a.drep = nil
			}
		}
	case *common.AuthCommitteeHotCertificate:
		if m, ok := s.committee[keyOf(c.ColdCredential)]; ok {
			hot := keyOf(c.HotCredential)
			m.hot = &hot
		}
	case *common.ResignCommitteeColdCertificate:
		if m, ok := s.committee[keyOf(c.ColdCredential)]; ok {
			m.hot, m.resigned = nil, true
		}
	}
}

func (s *certState) delegatePool(cred common.Credential, p common.PoolKeyHash) {
	if a, ok := s.accounts[keyOf(cred)]; ok {
		a.pool = &p
	}
}

func (s *certState) delegateVote(cred common.Credential, d common.Drep) {
	if a, ok := s.accounts[keyOf(cred)]; ok {
		a.drep = &d
	}
}

// registerPool records a first registration with its deposit, or replaces
// the parameters of a registered pool and cancels a pending retirement. The
// ledger defers a re-registration's parameters to the next epoch; the fake
// applies them at once, which no rule the fake runs can observe.
func (s *certState) registerPool(c *common.PoolRegistrationCertificate, r effects) {
	if p, ok := s.pools[c.Operator]; ok {
		p.reg, p.retire = *c, nil
		return
	}
	delete(s.epoch.retired, c.Operator)
	s.pools[c.Operator] = &pool{reg: *c, deposit: uint64(r.pp.PoolDeposit)}
}

func drepKey(d common.Drep) credKey {
	var h common.Blake2b224
	copy(h[:], d.Credential)
	kind := uint(common.CredentialTypeAddrKeyHash)
	if d.Type == common.DrepTypeScriptHash {
		kind = common.CredentialTypeScriptHash
	}
	return credKey{kind, h}
}

func actionType(a common.GovAction) (common.GovActionType, bool) {
	switch a.(type) {
	case common.ParameterChangeGovAction:
		return common.GovActionTypeParameterChange, true
	case *common.HardForkInitiationGovAction:
		return common.GovActionTypeHardForkInitiation, true
	case *common.TreasuryWithdrawalGovAction:
		return common.GovActionTypeTreasuryWithdrawal, true
	case *common.NoConfidenceGovAction:
		return common.GovActionTypeNoConfidence, true
	case *common.UpdateCommitteeGovAction:
		return common.GovActionTypeUpdateCommittee, true
	case *common.NewConstitutionGovAction:
		return common.GovActionTypeNewConstitution, true
	case *common.InfoGovAction:
		return common.GovActionTypeInfo, true
	}
	return 0, false
}

// sortedCreds returns the keys of m in a fixed order, so lookups that list
// several credentials answer deterministically.
func sortedCreds[V any](m map[credKey]V) []credKey {
	ks := slices.Collect(maps.Keys(m))
	slices.SortFunc(ks, func(a, b credKey) int {
		if c := cmp.Compare(a.kind, b.kind); c != 0 {
			return c
		}
		return bytes.Compare(a.hash[:], b.hash[:])
	})
	return ks
}
