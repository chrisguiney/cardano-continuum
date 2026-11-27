package build

import (
	"context"
	"fmt"
	"math/big"

	"github.com/Salvionied/apollo/v2"
	"github.com/blinklabs-io/gouroboros/cbor"
	"github.com/blinklabs-io/gouroboros/ledger/common"

	"github.com/chrisguiney/cardano-continuum/internal/keys"
)

// Pool builds the stake-pool transactions: registration, update and retirement.
type Pool struct{ b *Builder }

// PoolRegistration registers (or updates) a stake pool owned by the
// wallet: its stake key is the sole owner and the reward account.
type PoolRegistration struct {
	Spend
	// Cold is the pool cold key; its key hash is the pool id.
	Cold       keys.Signer
	VRFKeyHash [32]byte
	Pledge     uint64
	Cost       uint64
	Margin     *big.Rat
	Relays     []common.PoolRelay
	Metadata   *common.PoolMetadata
}

// PoolRetirement schedules a pool's retirement at the start of Epoch.
type PoolRetirement struct {
	Spend
	Cold         keys.Signer
	Epoch        uint64
	CurrentEpoch uint64
}

// RetirementEpochError reports a retirement epoch outside
// (current, current + eMax].
type RetirementEpochError struct {
	Epoch, Current, Max uint64
}

func (e *RetirementEpochError) Error() string {
	return fmt.Sprintf("build: retirement epoch %d outside (%d, %d]", e.Epoch, e.Current, e.Current+e.Max)
}

// RegisterPool registers a new pool, paying the pool deposit. The cold key,
// the owner's stake key and the payment key witness it.
func (f Pool) RegisterPool(ctx context.Context, w keys.Wallet, p PoolRegistration) (Built, error) {
	return f.pool(ctx, w, p, false)
}

// UpdatePool re-registers an existing pool with new parameters; no deposit
// is taken.
func (f Pool) UpdatePool(ctx context.Context, w keys.Wallet, p PoolRegistration) (Built, error) {
	return f.pool(ctx, w, p, true)
}

func (f Pool) pool(ctx context.Context, w keys.Wallet, p PoolRegistration, update bool) (Built, error) {
	cert, err := f.poolCert(w, p)
	if err != nil {
		return Built{}, err
	}
	need := f.b.params.PoolDeposit
	if update {
		need = 0
	}
	return f.b.finish(ctx, w, spec{
		sp: p.Spend, need: need, signers: []keys.Signer{w.StakeSigner(), p.Cold},
		add: func(a *apollo.Apollo) (*apollo.Apollo, error) {
			if update {
				return a.UpdatePool(cert), nil
			}
			return a.RegisterPool(cert), nil
		},
	})
}

func (f Pool) poolCert(w keys.Wallet, p PoolRegistration) (common.PoolRegistrationCertificate, error) {
	if p.Margin == nil || p.Margin.Sign() < 0 || p.Margin.Cmp(big.NewRat(1, 1)) > 0 {
		return common.PoolRegistrationCertificate{}, fmt.Errorf("build: pool margin %v outside [0, 1]", p.Margin)
	}
	owner := w.StakeKeyHash()
	c := common.PoolRegistrationCertificate{
		CertType:     uint(common.CertificateTypePoolRegistration),
		Operator:     common.PoolKeyHash(p.Cold.KeyHash()),
		VrfKeyHash:   p.VRFKeyHash,
		Pledge:       p.Pledge,
		Cost:         p.Cost,
		Margin:       cbor.Rat{Rat: new(big.Rat).Set(p.Margin)},
		PoolOwners:   []common.AddrKeyHash{common.AddrKeyHash(owner)},
		Relays:       p.Relays,
		PoolMetadata: p.Metadata,
	}
	if err := c.SetRewardAccountCredential(stakeCred(w), uint(f.b.params.NetworkID)); err != nil {
		return common.PoolRegistrationCertificate{}, fmt.Errorf("build: pool reward account: %w", err)
	}
	return c, nil
}

// RetirePool schedules the pool's retirement. The cold key witnesses it.
func (f Pool) RetirePool(ctx context.Context, w keys.Wallet, r PoolRetirement) (Built, error) {
	if r.Epoch <= r.CurrentEpoch || r.Epoch > r.CurrentEpoch+f.b.params.MaxRetireEpochs {
		return Built{}, &RetirementEpochError{Epoch: r.Epoch, Current: r.CurrentEpoch, Max: f.b.params.MaxRetireEpochs}
	}
	pool := common.Blake2b224(r.Cold.KeyHash())
	return f.b.finish(ctx, w, spec{
		sp: r.Spend, signers: []keys.Signer{r.Cold},
		add: func(a *apollo.Apollo) (*apollo.Apollo, error) { return a.DeregisterPool(pool, r.Epoch), nil },
	})
}
