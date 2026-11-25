package build

import (
	"context"

	"github.com/Salvionied/apollo/v2"
	"github.com/blinklabs-io/gouroboros/ledger/common"

	"github.com/chrisguiney/cardano-continuum/internal/keys"
)

// Delegation delegates the wallet's stake to a pool.
type Delegation struct {
	Spend
	Pool [28]byte
	// Register also registers the stake credential, paying the key deposit,
	// in one certificate.
	Register bool
}

// VoteDelegation delegates the wallet's voting power to a DRep (or to the
// always-abstain or always-no-confidence options).
type VoteDelegation struct {
	Spend
	DRep common.Drep
}

// Withdrawal withdraws Amount from the wallet's reward account. The ledger
// requires the whole balance; zero is valid and is how scripts and wallets
// with no rewards trigger the withdrawal path.
type Withdrawal struct {
	Spend
	Amount uint64
}

func stakeCred(w keys.Wallet) common.Credential {
	return common.Credential{CredType: common.CredentialTypeAddrKeyHash, Credential: common.Blake2b224(w.StakeKeyHash())}
}

func certType(c common.Certificate) uint { return uint(c.Type()) }

// RegisterStake registers the wallet's stake credential with a Conway
// reg_cert carrying the key deposit. The stake key witnesses it.
func (b *Builder) RegisterStake(ctx context.Context, w keys.Wallet, sp Spend) (Built, error) {
	return b.finish(ctx, w, b.stakeSpec(sp, w, []keys.Signer{w.StakeSigner()}))
}

func (b *Builder) stakeSpec(sp Spend, w keys.Wallet, signers []keys.Signer) spec {
	return spec{
		sp: sp, signers: signers, need: b.params.KeyDeposit,
		certs: []common.Certificate{&common.RegistrationCertificate{
			CertType: uint(common.CertificateTypeRegistration), StakeCredential: stakeCred(w), Amount: b.params.KeyDeposit,
		}},
	}
}

// DeregisterStake deregisters the wallet's stake credential, refunding the
// key deposit to the change output.
func (b *Builder) DeregisterStake(ctx context.Context, w keys.Wallet, sp Spend) (Built, error) {
	return b.finish(ctx, w, spec{
		sp: sp, signers: []keys.Signer{w.StakeSigner()},
		certs: []common.Certificate{&common.DeregistrationCertificate{
			CertType: uint(common.CertificateTypeDeregistration), StakeCredential: stakeCred(w), Amount: b.params.KeyDeposit,
		}},
	})
}

// Delegate delegates the wallet's stake to d.Pool.
func (b *Builder) Delegate(ctx context.Context, w keys.Wallet, d Delegation) (Built, error) {
	cred := stakeCred(w)
	var c common.Certificate = &common.StakeDelegationCertificate{
		CertType: uint(common.CertificateTypeStakeDelegation), StakeCredential: &cred, PoolKeyHash: d.Pool,
	}
	var need uint64
	if d.Register {
		need = b.params.KeyDeposit
		c = &common.StakeRegistrationDelegationCertificate{
			CertType:        uint(common.CertificateTypeStakeRegistrationDelegation),
			StakeCredential: cred, PoolKeyHash: d.Pool, Amount: b.params.KeyDeposit,
		}
	}
	return b.finish(ctx, w, spec{sp: d.Spend, signers: []keys.Signer{w.StakeSigner()}, need: need, certs: []common.Certificate{c}})
}

// DelegateVote delegates the wallet's voting power to d.DRep.
func (b *Builder) DelegateVote(ctx context.Context, w keys.Wallet, d VoteDelegation) (Built, error) {
	return b.finish(ctx, w, spec{
		sp: d.Spend, signers: []keys.Signer{w.StakeSigner()},
		certs: []common.Certificate{&common.VoteDelegationCertificate{
			CertType: uint(common.CertificateTypeVoteDelegation), StakeCredential: stakeCred(w), Drep: d.DRep,
		}},
	})
}

// Withdraw withdraws from the wallet's reward account into the change
// output. The stake key witnesses it.
func (b *Builder) Withdraw(ctx context.Context, w keys.Wallet, wd Withdrawal) (Built, error) {
	return b.finish(ctx, w, spec{
		sp: wd.Spend, signers: []keys.Signer{w.StakeSigner()},
		add: func(a *apollo.Apollo) (*apollo.Apollo, error) {
			return a.AddWithdrawal(w.StakeAddress(), wd.Amount, nil, nil), nil
		},
	})
}
