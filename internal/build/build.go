// Package build turns intents into signed Conway transactions. Every
// transaction it produces is valid by construction: Apollo refuses to build an
// invalid one, and deliberate mistakes are injected after the build, not here.
//
// Each intent family lives in its own file and adds a method to Builder; the
// shared plumbing (protocol parameters, the Apollo chain-context adapter, body
// signing) is in this file.
package build

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/Salvionied/apollo/v2"
	"github.com/Salvionied/apollo/v2/backend"
	"github.com/Salvionied/apollo/v2/backend/fixed"
	"github.com/blinklabs-io/gouroboros/cbor"
	"github.com/blinklabs-io/gouroboros/ledger"
	"github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/gouroboros/ledger/shelley"

	"github.com/chrisguiney/cardano-continuum/internal/keys"
)

// Params are the protocol parameters the builder needs. They are inputs, never
// constants, so the same builder serves any network configuration.
type Params struct {
	MinFeeA          uint64 // lovelace per byte
	MinFeeB          uint64 // constant lovelace
	CoinsPerUtxoByte uint64
	MaxTxSize        uint32
	NetworkID        uint8
}

// Ref names a transaction output.
type Ref = string

// Built is a signed transaction and the facts callers track about it.
type Built struct {
	Tx      []byte
	ID      common.Blake2b256
	Inputs  []Ref
	Outputs []common.Utxo
	Fee     uint64
	Signers []keys.Credential
	TTL     uint64
}

// InsufficientError reports that the offered inputs cannot cover the request.
type InsufficientError struct {
	Have, Need uint64
	Shortfall  uint64
}

func (e *InsufficientError) Error() string {
	return fmt.Sprintf("insufficient funds: have %d, need %d (short %d)", e.Have, e.Need, e.Shortfall)
}

// Builder builds transactions for one network.
type Builder struct {
	params Params
}

// New returns a Builder for the given protocol parameters.
func New(p Params) *Builder { return &Builder{params: p} }

// chainContext adapts Params to Apollo's chain context. It serves no UTxOs, so
// Apollo can only spend what the caller offers through AddInput.
func (b *Builder) chainContext() *fixed.FixedChainContext {
	p := b.params
	pp := backend.ProtocolParameters{
		MinFeeCoefficient:   int64(p.MinFeeA),
		MinFeeConstant:      int64(p.MinFeeB),
		MaxTxSize:           int(p.MaxTxSize),
		CoinsPerUtxoByte:    strconv.FormatUint(p.CoinsPerUtxoByte, 10),
		MaxValSize:          "5000",
		KeyDeposits:         "2000000",
		PoolDeposits:        "500000000",
		CollateralPercent:   150,
		MaxCollateralInputs: 3,
		MaxTxExMem:          "14000000",
		MaxTxExSteps:        "10000000000",
		PriceMem:            0.0577,
		PriceStep:           0.0000721,
	}
	return fixed.NewFixedChainContext(pp, backend.GenesisParameters{}, p.NetworkID)
}

// walletAdapter satisfies apollo.Wallet for address and key-hash lookups.
// Signing is done by sign, because keys.Wallet hashes the body itself while
// Apollo hands over an already-computed hash.
type walletAdapter struct{ w keys.Wallet }

func (a walletAdapter) Address() common.Address { return a.w.Address() }

func (a walletAdapter) PubKeyHash() common.Blake2b224 {
	return common.Blake2b224(a.w.PaymentKeyHash())
}

func (a walletAdapter) StakePubKeyHash() common.Blake2b224 {
	return common.Blake2b224(a.w.StakeKeyHash())
}

func (walletAdapter) SignTxBody(common.Blake2b256) (common.VkeyWitness, error) {
	return common.VkeyWitness{}, errors.New("build: signing goes through keys.Wallet.SignTxBody")
}

// finish builds with mk, then pins the fee to the ledger's exact minimum.
//
// Apollo prices the transaction with len(cbor), which counts the IsValid byte
// the ledger leaves out of the fee-relevant size, so its fee is a*1 too high.
// Rebuilding with ForceFee(a*TxSize+b) until the fee is stable gives the exact
// minimum when the transaction has change. A transaction whose change was
// folded into the fee keeps the higher fee, which is still valid.
func (b *Builder) finish(ctx context.Context, w keys.Wallet, mk func() *apollo.Apollo) (Built, error) {
	built, err := b.sign(ctx, mk(), w)
	if err != nil {
		return Built{}, err
	}
	const maxPasses = 4
	for range maxPasses {
		tx, err := ledger.NewTransactionFromCbor(uint(ledger.TxTypeConway), built.Tx)
		if err != nil {
			return Built{}, fmt.Errorf("build: decode: %w", err)
		}
		size, err := common.TxSize(tx)
		if err != nil {
			return Built{}, fmt.Errorf("build: size: %w", err)
		}
		exact := b.params.MinFeeA*uint64(size) + b.params.MinFeeB
		if built.Fee == exact || len(built.Outputs) < 2 {
			return built, nil
		}
		next, err := b.sign(ctx, mk().ForceFee(int64(exact)), w)
		if err != nil {
			return Built{}, err
		}
		built = next
	}
	return built, nil
}

// sign completes the transaction and signs its body with w.
func (b *Builder) sign(ctx context.Context, a *apollo.Apollo, w keys.Wallet) (Built, error) {
	a, err := a.WithContext(ctx).Complete()
	if err != nil {
		return Built{}, fmt.Errorf("build: %w", err)
	}
	tx := a.GetTx()
	body, err := cbor.Encode(&tx.Body)
	if err != nil {
		return Built{}, fmt.Errorf("build: encode body: %w", err)
	}
	wit := w.SignTxBody(body)
	if a, err = a.AddVerificationKeyWitness(common.VkeyWitness{Vkey: wit.VKey, Signature: wit.Signature}); err != nil {
		return Built{}, fmt.Errorf("build: witness: %w", err)
	}
	raw, err := a.GetTxCbor()
	if err != nil {
		return Built{}, fmt.Errorf("build: encode tx: %w", err)
	}
	tx = a.GetTx()
	built := Built{
		Tx:      raw,
		ID:      common.Blake2b256Hash(body),
		Fee:     tx.Fee().Uint64(),
		Signers: []keys.Credential{w.PaymentKeyHash()},
		TTL:     tx.TTL(),
	}
	for _, in := range tx.Inputs() {
		built.Inputs = append(built.Inputs, in.String())
	}
	for i, o := range tx.Outputs() {
		built.Outputs = append(built.Outputs, common.Utxo{
			Id:     shelley.ShelleyTransactionInput{TxId: built.ID, OutputIndex: uint32(i)},
			Output: o,
		})
	}
	return built, nil
}
