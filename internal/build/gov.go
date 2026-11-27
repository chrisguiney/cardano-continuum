package build

import (
	"context"
	"fmt"

	"github.com/Salvionied/apollo/v2"
	"github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/gouroboros/ledger/conway"

	"github.com/chrisguiney/cardano-continuum/internal/keys"
)

// Gov builds the governance transactions: DRep registration, hot-key authorisation, votes and proposals.
type Gov struct{ b *Builder }

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

// Proposal submits a governance action of type Action (info when
// empty), paying the governance action deposit, which is returned to the
// wallet's reward account. That account must be registered.
type Proposal struct {
	Spend
	Action ActionType
	Anchor common.GovAnchor
}

// ActionType is a Conway governance action type, by the names profile
// files use (profile.Network.Proposals).
type ActionType string

// The action types Propose builds. A hard fork needs a protocol version
// the devnet can move to and a committee update a membership plan, so
// neither is built; the rest name no previous action, which is right while
// no action of their purpose was ever enacted, and the fake chain enacts
// none.
const (
	ActionInfo                ActionType = "info"
	ActionParameterChange     ActionType = "parameter_change"
	ActionTreasuryWithdrawals ActionType = "treasury_withdrawals"
	ActionNoConfidence        ActionType = "no_confidence"
	ActionNewConstitution     ActionType = "new_constitution"
)

// treasuryAsk is what a treasury withdrawal proposal asks for. The
// proposal is never enacted, so the amount only has to be positive
// (ZeroTreasuryWithdrawals rejects zero).
const treasuryAsk = 1_000_000

// constitutionAnchor is what a new-constitution proposal points at. Nothing
// fetches it.
var constitutionAnchor = common.GovAnchor{Url: "https://example.invalid/constitution", DataHash: common.Blake2b256Hash([]byte("continuum constitution"))}

// DRepRetirement deregisters a key-hash DRep, refunding its deposit.
type DRepRetirement struct {
	Spend
	DRep keys.Signer
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
func (f Gov) RegisterDRep(ctx context.Context, w keys.Wallet, r DRepRegistration) (Built, error) {
	return f.b.finish(ctx, w, spec{
		sp: r.Spend, need: f.b.params.DRepDeposit, signers: []keys.Signer{r.DRep},
		certs: []common.Certificate{&common.RegistrationDrepCertificate{
			CertType:       uint(common.CertificateTypeRegistrationDrep),
			DrepCredential: keyCred(r.DRep.KeyHash()), Amount: f.b.params.DRepDeposit, Anchor: r.Anchor,
		}},
	})
}

// RetireDRep deregisters the DRep key; the deposit returns in the change.
// The DRep key witnesses it.
func (f Gov) RetireDRep(ctx context.Context, w keys.Wallet, r DRepRetirement) (Built, error) {
	return f.b.finish(ctx, w, spec{
		sp: r.Spend, signers: []keys.Signer{r.DRep},
		certs: []common.Certificate{&common.DeregistrationDrepCertificate{
			CertType:       uint(common.CertificateTypeDeregistrationDrep),
			DrepCredential: keyCred(r.DRep.KeyHash()), Amount: f.b.params.DRepDeposit,
		}},
	})
}

// AuthorizeHot authorises a committee hot key. The cold key witnesses it.
func (f Gov) AuthorizeHot(ctx context.Context, w keys.Wallet, h HotAuth) (Built, error) {
	return f.b.finish(ctx, w, spec{
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
func (f Gov) Vote(ctx context.Context, w keys.Wallet, v Vote) (Built, error) {
	t, ok := voterTypes[v.Role]
	if !ok {
		return Built{}, fmt.Errorf("build: unknown voter role %d", v.Role)
	}
	if v.Choice > common.GovVoteAbstain {
		return Built{}, fmt.Errorf("build: unknown vote %d", v.Choice)
	}
	voter := common.Voter{Type: t, Hash: v.Voter.KeyHash()}
	return f.b.finish(ctx, w, spec{
		sp: v.Spend, signers: []keys.Signer{v.Voter},
		add: func(a *apollo.Apollo) (*apollo.Apollo, error) {
			return a.AddVote(voter, v.Action, common.VotingProcedure{Vote: v.Choice, Anchor: v.Anchor}), nil
		},
	})
}

// Propose submits a governance action. A parameter change re-states the
// current maximum transaction size, so enacting it would change nothing;
// a treasury withdrawal pays the wallet's own reward account. Neither
// names a guardrail policy: the devnet constitution has none.
func (f Gov) Propose(ctx context.Context, w keys.Wallet, p Proposal) (Built, error) {
	act, err := f.govAction(w, p.Action)
	if err != nil {
		return Built{}, err
	}
	proc := conway.ConwayProposalProcedure{
		PPDeposit:       f.b.params.GovActionDeposit,
		PPRewardAccount: w.StakeAddress(),
		PPGovAction:     act,
		PPAnchor:        p.Anchor,
	}
	return f.b.finish(ctx, w, spec{
		sp: p.Spend, need: f.b.params.GovActionDeposit,
		add: func(a *apollo.Apollo) (*apollo.Apollo, error) { return a.AddProposal(proc), nil },
	})
}

func (f Gov) govAction(w keys.Wallet, t ActionType) (conway.ConwayGovAction, error) {
	switch t {
	case "", ActionInfo:
		info, err := common.NewInfoGovAction()
		if err != nil {
			return conway.ConwayGovAction{}, fmt.Errorf("build: info action: %w", err)
		}
		return conway.ConwayGovAction{Type: uint(common.GovActionTypeInfo), Action: info}, nil
	case ActionParameterChange:
		size := uint(f.b.params.MaxTxSize)
		return conway.ConwayGovAction{Type: uint(common.GovActionTypeParameterChange), Action: &conway.ConwayParameterChangeGovAction{
			Type: uint(common.GovActionTypeParameterChange), ParamUpdate: conway.ConwayProtocolParameterUpdate{MaxTxSize: &size},
		}}, nil
	case ActionTreasuryWithdrawals:
		to := w.StakeAddress()
		return conway.ConwayGovAction{Type: uint(common.GovActionTypeTreasuryWithdrawal), Action: &common.TreasuryWithdrawalGovAction{
			Type: uint(common.GovActionTypeTreasuryWithdrawal), Withdrawals: map[*common.Address]uint64{&to: treasuryAsk},
		}}, nil
	case ActionNoConfidence:
		return conway.ConwayGovAction{Type: uint(common.GovActionTypeNoConfidence), Action: &common.NoConfidenceGovAction{
			Type: uint(common.GovActionTypeNoConfidence),
		}}, nil
	case ActionNewConstitution:
		c := &common.NewConstitutionGovAction{Type: uint(common.GovActionTypeNewConstitution)}
		c.Constitution.Anchor = constitutionAnchor
		return conway.ConwayGovAction{Type: uint(common.GovActionTypeNewConstitution), Action: c}, nil
	}
	return conway.ConwayGovAction{}, fmt.Errorf("build: unsupported governance action type %q", t)
}
