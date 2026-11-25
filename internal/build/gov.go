package build

import (
	"context"
	"fmt"

	"github.com/Salvionied/apollo/v2"
	"github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/gouroboros/ledger/conway"

	"github.com/chrisguiney/cardano-continuum/internal/keys"
)

// DRepRegistration registers a key-hash DRep, paying the DRep deposit.
type DRepRegistration struct {
	Spend
	DRep   keys.Signer
	Anchor *common.GovAnchor
}

// Role is the kind of voter a Vote speaks for.
type Role int

// Voter roles.
const (
	RoleDRep Role = iota + 1
	RoleCommittee
	RolePool
)

// Vote casts Choice (common.GovVoteNo, Yes or Abstain) on Action. Voter is
// the DRep key, the committee hot key or the pool cold key, by Role.
type Vote struct {
	Spend
	Voter  keys.Signer
	Role   Role
	Action common.GovActionId
	Choice uint8
	Anchor *common.GovAnchor
}

// Proposal submits an info action, paying the governance action deposit,
// which is returned to the wallet's reward account. That account must be
// registered.
type Proposal struct {
	Spend
	Anchor common.GovAnchor
}

// HotAuth authorises a hot key for committee member Cold.
type HotAuth struct {
	Spend
	Cold keys.Signer
	Hot  keys.Credential
}

func keyCred(c keys.Credential) common.Credential {
	return common.Credential{CredType: common.CredentialTypeAddrKeyHash, Credential: common.Blake2b224(c)}
}

// RegisterDRep registers the DRep key. The DRep key witnesses it.
func (b *Builder) RegisterDRep(ctx context.Context, w keys.Wallet, r DRepRegistration) (Built, error) {
	return b.finish(ctx, w, spec{
		sp: r.Spend, need: b.params.DRepDeposit, signers: []keys.Signer{r.DRep},
		certs: []common.Certificate{&common.RegistrationDrepCertificate{
			CertType:       uint(common.CertificateTypeRegistrationDrep),
			DrepCredential: keyCred(r.DRep.KeyHash()), Amount: b.params.DRepDeposit, Anchor: r.Anchor,
		}},
	})
}

// AuthorizeHot authorises a committee hot key. The cold key witnesses it.
func (b *Builder) AuthorizeHot(ctx context.Context, w keys.Wallet, h HotAuth) (Built, error) {
	return b.finish(ctx, w, spec{
		sp: h.Spend, signers: []keys.Signer{h.Cold},
		certs: []common.Certificate{&common.AuthCommitteeHotCertificate{
			CertType:       uint(common.CertificateTypeAuthCommitteeHot),
			ColdCredential: keyCred(h.Cold.KeyHash()), HotCredential: keyCred(h.Hot),
		}},
	})
}

var voterTypes = map[Role]uint8{
	RoleDRep:      common.VoterTypeDRepKeyHash,
	RoleCommittee: common.VoterTypeConstitutionalCommitteeHotKeyHash,
	RolePool:      common.VoterTypeStakingPoolKeyHash,
}

// Vote casts a vote. The voter's key witnesses it.
func (b *Builder) Vote(ctx context.Context, w keys.Wallet, v Vote) (Built, error) {
	t, ok := voterTypes[v.Role]
	if !ok {
		return Built{}, fmt.Errorf("build: unknown voter role %d", v.Role)
	}
	if v.Choice > common.GovVoteAbstain {
		return Built{}, fmt.Errorf("build: unknown vote %d", v.Choice)
	}
	voter := common.Voter{Type: t, Hash: v.Voter.KeyHash()}
	return b.finish(ctx, w, spec{
		sp: v.Spend, signers: []keys.Signer{v.Voter},
		add: func(a *apollo.Apollo) (*apollo.Apollo, error) {
			return a.AddVote(voter, v.Action, common.VotingProcedure{Vote: v.Choice, Anchor: v.Anchor}), nil
		},
	})
}

// Propose submits an info action.
func (b *Builder) Propose(ctx context.Context, w keys.Wallet, p Proposal) (Built, error) {
	info, err := common.NewInfoGovAction()
	if err != nil {
		return Built{}, fmt.Errorf("build: info action: %w", err)
	}
	proc := conway.ConwayProposalProcedure{
		PPDeposit:       b.params.GovActionDeposit,
		PPRewardAccount: w.StakeAddress(),
		PPGovAction:     conway.ConwayGovAction{Type: uint(common.GovActionTypeInfo), Action: info},
		PPAnchor:        p.Anchor,
	}
	return b.finish(ctx, w, spec{
		sp: p.Spend, need: b.params.GovActionDeposit,
		add: func(a *apollo.Apollo) (*apollo.Apollo, error) { return a.AddProposal(proc), nil },
	})
}
