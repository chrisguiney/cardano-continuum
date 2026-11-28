package build

import (
	"context"

	"github.com/Salvionied/apollo/v2"
	"github.com/blinklabs-io/gouroboros/ledger/common"

	"github.com/chrisguiney/cardano-continuum/internal/keys"
)

// Stake builds the stake-key transactions: registration, delegation and withdrawal.
type Stake struct{ b *Builder }

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
	// Into is a script the withdrawn lovelace is paid to, with Datum
	// inline, instead of the change: a treasury award locked for
	// milestone draws. Nil pays the change.
	Into  *Script
	Datum common.Datum
}

func stakeCred(w keys.Wallet) common.Credential {
	return common.Credential{CredType: common.CredentialTypeAddrKeyHash, Credential: common.Blake2b224(w.StakeKeyHash())}
}

func certType(c common.Certificate) uint { return uint(c.Type()) }

// RegisterStake registers the wallet's stake credential with a Conway
// reg_cert carrying the key deposit. The stake key witnesses it.
func (f Stake) RegisterStake(ctx context.Context, w keys.Wallet, sp Spend) (Built, error) {
	return f.b.finish(ctx, w, f.stakeSpec(sp, w, []keys.Signer{w.StakeSigner()}))
}

func (f Stake) stakeSpec(sp Spend, w keys.Wallet, signers []keys.Signer) spec {
	return spec{
		sp: sp, signers: signers, need: f.b.params.KeyDeposit,
		certs: []common.Certificate{&common.RegistrationCertificate{
			CertType: uint(common.CertificateTypeRegistration), StakeCredential: stakeCred(w), Amount: f.b.params.KeyDeposit,
		}},
	}
}

// DeregisterStake deregisters the wallet's stake credential, refunding the
// key deposit to the change output.
func (f Stake) DeregisterStake(ctx context.Context, w keys.Wallet, sp Spend) (Built, error) {
	return f.b.finish(ctx, w, spec{
		sp: sp, signers: []keys.Signer{w.StakeSigner()},
		certs: []common.Certificate{&common.DeregistrationCertificate{
			CertType: uint(common.CertificateTypeDeregistration), StakeCredential: stakeCred(w), Amount: f.b.params.KeyDeposit,
		}},
	})
}

// Delegate delegates the wallet's stake to d.Pool.
func (f Stake) Delegate(ctx context.Context, w keys.Wallet, d Delegation) (Built, error) {
	cred := stakeCred(w)
	var c common.Certificate = &common.StakeDelegationCertificate{
		CertType: uint(common.CertificateTypeStakeDelegation), StakeCredential: &cred, PoolKeyHash: d.Pool,
	}
	var need uint64
	if d.Register {
		need = f.b.params.KeyDeposit
		c = &common.StakeRegistrationDelegationCertificate{
			CertType:        uint(common.CertificateTypeStakeRegistrationDelegation),
			StakeCredential: cred, PoolKeyHash: d.Pool, Amount: f.b.params.KeyDeposit,
		}
	}
	return f.b.finish(ctx, w, spec{sp: d.Spend, signers: []keys.Signer{w.StakeSigner()}, need: need, certs: []common.Certificate{c}})
}

// DelegateVote delegates the wallet's voting power to d.DRep.
func (f Stake) DelegateVote(ctx context.Context, w keys.Wallet, d VoteDelegation) (Built, error) {
	return f.b.finish(ctx, w, spec{
		sp: d.Spend, signers: []keys.Signer{w.StakeSigner()},
		certs: []common.Certificate{&common.VoteDelegationCertificate{
			CertType: uint(common.CertificateTypeVoteDelegation), StakeCredential: stakeCred(w), Drep: d.DRep,
		}},
	})
}

// Withdraw withdraws from the wallet's reward account into the change
// output. The stake key witnesses it.
func (f Stake) Withdraw(ctx context.Context, w keys.Wallet, wd Withdrawal) (Built, error) {
	return f.b.finish(ctx, w, spec{
		sp: wd.Spend, signers: []keys.Signer{w.StakeSigner()},
		add: func(a *apollo.Apollo) (*apollo.Apollo, error) {
			a = a.AddWithdrawal(w.StakeAddress(), wd.Amount, nil, nil)
			if wd.Into != nil {
				a = a.PayToContract(wd.Into.Address(f.b.params.NetworkID), &wd.Datum, int64(wd.Amount))
			}
			return a, nil
		},
	})
}
