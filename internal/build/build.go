// Package build turns intents into signed Conway transactions. Every
// transaction it produces is valid by construction: Apollo refuses to build an
// invalid one, and deliberate mistakes are injected after the build, not here.
//
// There is one method per intent kind, each taking a typed request, because
// an agenda intent names only the kind, actor and target; the planner fills
// in the inputs and amounts. Payments stay on Builder; the other families
// hang off it:
//
//	send             Builder.Pay
//	register_stake   Stake().RegisterStake
//	delegate         Stake().Delegate (Register: true also registers)
//	undelegate       Stake().DeregisterStake
//	withdraw_rewards Stake().Withdraw
//	delegate_drep    Stake().DelegateVote
//	register_drep    Gov().RegisterDRep
//	vote             Gov().Vote
//	propose          Gov().Propose
//	register_pool    Pool().RegisterPool, Pool().UpdatePool
//	retire_pool      Pool().RetirePool
//	mint, burn       Plutus().Mint (negative Quantity burns)
//	script_call      Plutus().Lock, Plutus().Unlock
//
// Each intent family lives in its own file; the shared plumbing (protocol
// parameters, the Apollo chain-context adapter, signing and exact fees) is
// in this file, and the chain-context adapter for local script evaluation in
// eval.go.
package build

import (
	"cmp"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Salvionied/apollo/v2"
	"github.com/Salvionied/apollo/v2/backend"
	"github.com/Salvionied/apollo/v2/backend/fixed"
	"github.com/blinklabs-io/gouroboros/cbor"
	"github.com/blinklabs-io/gouroboros/ledger/babbage"
	"github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/gouroboros/ledger/common/script"
	"github.com/blinklabs-io/gouroboros/ledger/conway"
	"github.com/blinklabs-io/gouroboros/ledger/mary"
	"github.com/blinklabs-io/gouroboros/ledger/shelley"
	mockledger "github.com/blinklabs-io/ouroboros-mock/ledger"

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

	KeyDeposit       uint64
	PoolDeposit      uint64
	DRepDeposit      uint64
	GovActionDeposit uint64
	MinPoolCost      uint64
	// MaxRetireEpochs is eMax: how far ahead a pool may schedule retirement.
	MaxRetireEpochs uint64

	// Prices are the execution unit prices; nil prices make script
	// transactions fail to build.
	Prices       common.ExUnitPrice
	MaxTxExUnits common.ExUnits
	// MaxBlockExUnits is the per-block budget, which a transaction's
	// declared units must also fit to be forged.
	MaxBlockExUnits     common.ExUnits
	CollateralPercent   uint
	MaxCollateralInputs uint
	ProtocolMajor       uint
	// PlutusV3 is the PlutusV3 cost model in ledger parameter order.
	PlutusV3 []int64

	// SystemStart and SlotLength convert slots to POSIX time for script
	// contexts.
	SystemStart time.Time
	SlotLength  time.Duration
}

// ParamsFromConway takes Params from Conway protocol parameters.
func ParamsFromConway(pp *conway.ConwayProtocolParameters, networkID uint8) Params {
	return Params{
		MinFeeA: uint64(pp.MinFeeA), MinFeeB: uint64(pp.MinFeeB),
		CoinsPerUtxoByte: pp.AdaPerUtxoByte, MaxTxSize: uint32(pp.MaxTxSize), NetworkID: networkID,
		KeyDeposit: uint64(pp.KeyDeposit), PoolDeposit: uint64(pp.PoolDeposit),
		DRepDeposit: pp.DRepDeposit, GovActionDeposit: pp.GovActionDeposit,
		MinPoolCost: pp.MinPoolCost, MaxRetireEpochs: uint64(pp.MaxEpoch),
		Prices: pp.ExecutionCosts, MaxTxExUnits: pp.MaxTxExUnits, MaxBlockExUnits: pp.MaxBlockExUnits,
		CollateralPercent: pp.CollateralPercentage, MaxCollateralInputs: pp.MaxCollateralInputs,
		ProtocolMajor: pp.ProtocolVersion.Major, PlutusV3: pp.CostModels[2],
	}
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

// Spend is what every request carries: the UTxOs the builder may spend
// (choosing them is the planner's job; all are spent) and the absolute slot
// after which the transaction is invalid. Change goes to the wallet.
type Spend struct {
	Inputs []common.Utxo
	TTL    uint64
	// Tip is lovelace paid in fee above the ledger's minimum, taken from
	// the change: an adversary outbidding a transaction it races. Without
	// change the fee stays as built.
	Tip uint64
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

// Stake returns the builder for stake-key transactions.
func (b *Builder) Stake() Stake { return Stake{b} }

// Gov returns the builder for governance transactions.
func (b *Builder) Gov() Gov { return Gov{b} }

// Pool returns the builder for stake-pool transactions.
func (b *Builder) Pool() Pool { return Pool{b} }

// Plutus returns the builder for script transactions.
func (b *Builder) Plutus() Plutus { return Plutus{b} }

// spec is one transaction to build: the wallet paying fees and taking
// change, the offered inputs, the extra keys that must witness it beyond the
// payment key, and the intent-specific Apollo calls.
type spec struct {
	sp      Spend
	signers []keys.Signer
	// need is the lovelace the inputs must cover beyond the fee: payments
	// and deposits less refunds and withdrawals.
	need uint64
	// known are UTxOs the transaction spends that are not in sp.Inputs
	// (script inputs), so local evaluation can resolve them.
	known []common.Utxo
	certs []common.Certificate
	add   func(a *apollo.Apollo) (*apollo.Apollo, error)
	// minted is what the transaction mints (negative: burns), so the
	// tokens the wallet keeps can be paid back apart from the change.
	minted *apollo.Unit
}

// protocolParams renders Params as Apollo's protocol parameters.
func (b *Builder) protocolParams() backend.ProtocolParameters {
	p := b.params
	pp := backend.ProtocolParameters{
		MinFeeCoefficient:    int64(p.MinFeeA),
		MinFeeConstant:       int64(p.MinFeeB),
		MaxTxSize:            int(p.MaxTxSize),
		CoinsPerUtxoByte:     strconv.FormatUint(p.CoinsPerUtxoByte, 10),
		MaxValSize:           "5000",
		KeyDeposits:          strconv.FormatUint(p.KeyDeposit, 10),
		PoolDeposits:         strconv.FormatUint(p.PoolDeposit, 10),
		CollateralPercent:    int(p.CollateralPercent),
		MaxCollateralInputs:  int(p.MaxCollateralInputs),
		MaxTxExMem:           strconv.FormatInt(p.MaxTxExUnits.Memory, 10),
		MaxTxExSteps:         strconv.FormatInt(p.MaxTxExUnits.Steps, 10),
		ProtocolMajorVersion: int(p.ProtocolMajor),
	}
	// Apollo's prices are floats; they only seed its fee estimate, which
	// finish replaces with the exact ledger minimum.
	if p.Prices.MemPrice != nil && p.Prices.StepPrice != nil {
		pp.PriceMem, _ = p.Prices.MemPrice.Float64()
		pp.PriceStep, _ = p.Prices.StepPrice.Float64()
	}
	if len(p.PlutusV3) > 0 {
		pp.CostModels = map[string][]int64{"PlutusV3": p.PlutusV3}
	}
	return pp
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

// apollo returns a fresh Apollo for s, with an optional forced fee.
func (b *Builder) apollo(w keys.Wallet, s spec, fee int64) (*apollo.Apollo, error) {
	if s.sp.TTL > 1<<62 {
		return nil, fmt.Errorf("build: ttl %d out of range", s.sp.TTL)
	}
	a := apollo.New(b.chainContext(s)).
		SetWallet(walletAdapter{w}).
		SetChangeAddress(w.Address()).
		SetTtl(int64(s.sp.TTL))
	for _, u := range s.sp.Inputs {
		a = a.AddInput(u)
	}
	if len(s.certs) > 0 {
		ws := make([]common.CertificateWrapper, len(s.certs))
		for i, c := range s.certs {
			ws[i] = common.CertificateWrapper{Type: certType(c), Certificate: c}
		}
		a = a.SetCertificates(ws)
	}
	if s.add != nil {
		var err error
		if a, err = s.add(a); err != nil {
			return nil, fmt.Errorf("build: %w", err)
		}
	}
	if units := keptTokens(s.sp.Inputs, s.minted); len(units) > 0 {
		// Apollo would put the tokens in the change. Paid apart at their
		// min-UTxO, they leave the change pure ADA, which is what script
		// transactions take collateral from (collateral).
		a = a.PayToAddress(w.Address(), 0, units...)
	}
	if fee > 0 {
		a = a.ForceFee(fee)
	}
	return a, nil
}

// finish checks the offered value, builds s, then pins the fee to the
// ledger's exact minimum plus the spend's Tip.
//
// Apollo prices the transaction with len(cbor), which counts the IsValid byte
// the ledger leaves out of the fee-relevant size, so its fee is a*1 too high.
// Rebuilding with ForceFee(minimum) until the fee is stable gives the exact
// minimum when the transaction has change. The minimum is gouroboros'
// Conway MinFeeTx formula: a*size + b + the execution unit fee. A transaction
// whose change was folded into the fee keeps the higher fee, which is still
// valid.
func (b *Builder) finish(ctx context.Context, w keys.Wallet, s spec) (Built, error) {
	var have uint64
	for _, u := range s.sp.Inputs {
		have += u.Output.Amount().Uint64()
	}
	if have < s.need {
		return Built{}, &InsufficientError{Have: have, Need: s.need, Shortfall: s.need - have}
	}
	built, err := b.sign(ctx, w, s, 0)
	if err != nil {
		return Built{}, err
	}
	const maxPasses = 4
	for range maxPasses {
		exact, err := b.minFee(built.Tx)
		if err != nil {
			return Built{}, err
		}
		want := exact + s.sp.Tip
		if built.Fee == want || !hasChange(built, w) {
			return built, nil
		}
		if built, err = b.sign(ctx, w, s, int64(want)); err != nil {
			return Built{}, err
		}
	}
	return built, nil
}

func hasChange(b Built, w keys.Wallet) bool {
	addr := w.Address().String()
	for _, o := range b.Outputs {
		if o.Output.Address().String() == addr {
			return true
		}
	}
	return false
}

// minFee is the ledger's minimum fee for raw: a*TxSize + b plus
// ceiling(memPrice*mem + stepPrice*steps) summed over the redeemers
// (cardano-ledger Conway getMinFeeTx without reference scripts, which no
// builder uses).
func (b *Builder) minFee(raw []byte) (uint64, error) {
	tx, err := conway.NewConwayTransactionFromCbor(raw)
	if err != nil {
		return 0, fmt.Errorf("build: decode: %w", err)
	}
	size, err := common.TxSize(tx)
	if err != nil {
		return 0, fmt.Errorf("build: size: %w", err)
	}
	fee := b.params.MinFeeA*uint64(size) + b.params.MinFeeB
	w := tx.Witnesses()
	if w == nil || w.Redeemers() == nil {
		return fee, nil
	}
	var mem, steps int64
	for _, r := range w.Redeemers().Iter() {
		mem += r.ExUnits.Memory
		steps += r.ExUnits.Steps
	}
	if mem == 0 && steps == 0 {
		return fee, nil
	}
	pr := b.params.Prices
	if pr.MemPrice == nil || pr.StepPrice == nil {
		return 0, errors.New("build: execution unit prices unset")
	}
	cost := new(big.Rat).Mul(pr.MemPrice.Rat, big.NewRat(mem, 1))
	cost.Add(cost, new(big.Rat).Mul(pr.StepPrice.Rat, big.NewRat(steps, 1)))
	ceil := new(big.Int).Add(cost.Num(), new(big.Int).Sub(cost.Denom(), big.NewInt(1)))
	ceil.Quo(ceil, cost.Denom())
	return fee + ceil.Uint64(), nil
}

// sign completes the transaction and signs its body with the payment key and
// every key in s.signers.
func (b *Builder) sign(ctx context.Context, w keys.Wallet, s spec, fee int64) (Built, error) {
	a, err := b.apollo(w, s, fee)
	if err != nil {
		return Built{}, err
	}
	if a, err = a.WithContext(ctx).Complete(); err != nil {
		return Built{}, fmt.Errorf("build: %w", err)
	}
	tx := a.GetTx()
	body, err := cbor.Encode(&tx.Body)
	if err != nil {
		return Built{}, fmt.Errorf("build: encode body: %w", err)
	}
	signers := append([]keys.Signer{w.PaymentSigner()}, s.signers...)
	seen := map[keys.Credential]bool{}
	var who []keys.Credential
	for _, k := range signers {
		if seen[k.KeyHash()] {
			continue
		}
		seen[k.KeyHash()] = true
		who = append(who, k.KeyHash())
		wit := k.SignTxBody(body)
		if a, err = a.AddVerificationKeyWitness(common.VkeyWitness{Vkey: wit.VKey, Signature: wit.Signature}); err != nil {
			return Built{}, fmt.Errorf("build: witness: %w", err)
		}
	}
	raw, err := a.GetTxCbor()
	if err != nil {
		return Built{}, fmt.Errorf("build: encode tx: %w", err)
	}
	return describe(raw, a.GetTx(), common.Blake2b256Hash(body), who), nil
}

func describe(raw []byte, tx *conway.ConwayTransaction, id common.Blake2b256, who []keys.Credential) Built {
	built := Built{Tx: raw, ID: id, Fee: tx.Fee().Uint64(), Signers: who, TTL: tx.TTL()}
	for _, in := range tx.Inputs() {
		built.Inputs = append(built.Inputs, in.String())
	}
	for i, o := range tx.Outputs() {
		built.Outputs = append(built.Outputs, common.Utxo{
			Id:     shelley.ShelleyTransactionInput{TxId: id, OutputIndex: uint32(i)},
			Output: o,
		})
	}
	return built
}

// keptTokens are the tokens of the inputs plus what the transaction
// mints, by policy and name in sorted order; zero quantities drop out.
func keptTokens(inputs []common.Utxo, minted *apollo.Unit) []apollo.Unit {
	held := map[tokenKey]int64{}
	for _, u := range inputs {
		addHeld(held, u.Output.Assets())
	}
	if minted != nil {
		held[tokenKey{minted.PolicyId, minted.Name}] += minted.Quantity
	}
	var out []apollo.Unit
	for k, q := range held {
		if q > 0 {
			out = append(out, apollo.Unit{PolicyId: k.policy, Name: k.name, Quantity: q})
		}
	}
	slices.SortFunc(out, func(a, b apollo.Unit) int {
		return cmp.Or(strings.Compare(a.PolicyId, b.PolicyId), strings.Compare(a.Name, b.Name))
	})
	return out
}

// Pay builds and signs a payment, returning change to w's own address.
// Change below min-UTxO is folded into the fee. All Inputs are spent.
func (b *Builder) Pay(ctx context.Context, w keys.Wallet, p Payment) (Built, error) {
	return b.finish(ctx, w, spec{
		sp:   Spend{Inputs: p.Inputs, TTL: p.TTL, Tip: p.Tip},
		need: p.Amount,
		add: func(a *apollo.Apollo) (*apollo.Apollo, error) {
			return a.PayToAddress(p.To, int64(p.Amount)), nil
		},
	})
}

// minUtxoOverhead is the bytes CIP-55 adds to an output's size for its
// input and UTxO map entry (Conway's MinCoinTxOut).
const minUtxoOverhead = 160

// MinPayment is the smallest plain-ADA payment to the address the ledger
// accepts: coinsPerUTxOByte * (160 + size of the output). The size counts
// the coin itself, so it is found by iterating to a fixed point, which a
// CBOR integer's few size steps reach at once. The output is sized in
// Babbage's map form, the larger of the two, so the result is never
// below the ledger's minimum.
func (b *Builder) MinPayment(to common.Address) uint64 {
	coin := uint64(0)
	for range 4 {
		o := babbage.BabbageTransactionOutput{OutputAddress: to, OutputAmount: mary.MaryTransactionOutputValue{Amount: coin}}
		size, err := common.TransactionOutputCborSize(&o)
		if err != nil {
			// An address the builder holds always encodes.
			panic("build: size a payment output: " + err.Error())
		}
		next := b.params.CoinsPerUtxoByte * (minUtxoOverhead + size)
		if next == coin {
			break
		}
		coin = next
	}
	return coin
}

// FanOut builds and signs one transaction paying every output from w's
// inputs, returning change to w. A faucet refilling many wallets uses it, so
// one confirmation funds them all instead of one wallet per chained spend.
func (b *Builder) FanOut(ctx context.Context, w keys.Wallet, sp Spend, outs []Output) (Built, error) {
	if len(outs) == 0 {
		return Built{}, errors.New("build: fan-out with no outputs")
	}
	var need uint64
	for _, o := range outs {
		need += o.Amount
	}
	return b.finish(ctx, w, spec{
		sp:   sp,
		need: need,
		add: func(a *apollo.Apollo) (*apollo.Apollo, error) {
			for _, o := range outs {
				a = a.PayToAddress(o.To, int64(o.Amount))
			}
			return a, nil
		},
	})
}

func (b *Builder) chainContext(s spec) *evalContext {
	known := append(append([]common.Utxo(nil), s.sp.Inputs...), s.known...)
	return &evalContext{
		FixedChainContext: fixed.NewFixedChainContext(b.protocolParams(), backend.GenesisParameters{}, b.params.NetworkID),
		b:                 b,
		known:             known,
	}
}

// evaluate runs every redeemer of txCbor against the per-transaction budget
// and returns the units each one used. utxos resolve the inputs, collateral
// and reference inputs.
func (b *Builder) evaluate(txCbor []byte, utxos []common.Utxo) (map[common.RedeemerKey]common.ExUnits, error) {
	if len(b.params.PlutusV3) == 0 {
		return nil, errors.New("build: no PlutusV3 cost model")
	}
	if b.params.SlotLength <= 0 {
		return nil, errors.New("build: slot length unset; script contexts need slot times")
	}
	tx, err := conway.NewConwayTransactionFromCbor(txCbor)
	if err != nil {
		return nil, fmt.Errorf("build: evaluate: decode: %w", err)
	}
	byRef := make(map[string]common.Utxo, len(utxos))
	for _, u := range utxos {
		byRef[u.Id.String()] = u
	}
	start, slot := b.params.SystemStart, b.params.SlotLength
	ls := mockledger.NewLedgerStateBuilder().
		WithNetworkId(uint(b.params.NetworkID)).
		WithUtxoById(func(in common.TransactionInput) (common.Utxo, error) {
			if u, ok := byRef[in.String()]; ok {
				return u, nil
			}
			return common.Utxo{}, fmt.Errorf("build: evaluate: input %s not offered", in)
		}).
		WithSlotToTime(func(s uint64) (time.Time, error) { return start.Add(time.Duration(s) * slot), nil }).
		WithTimeToSlot(func(t time.Time) (uint64, error) { return uint64(t.Sub(start) / slot), nil }).
		Build()
	budget := b.params.MaxTxExUnits
	used, err := script.EvaluatePlutusScripts(tx, ls, b.params.ProtocolMajor,
		map[uint][]int64{2: b.params.PlutusV3}, 3, conway.EraNameConway, &budget)
	if err != nil {
		return nil, fmt.Errorf("build: evaluate: %w", err)
	}
	return used, nil
}

// tokenKey identifies one native asset by hex policy and hex name.
type tokenKey struct{ policy, name string }

// addHeld adds every asset of as to held.
func addHeld(held map[tokenKey]int64, as *common.MultiAsset[common.MultiAssetTypeOutput]) {
	if as == nil {
		return
	}
	for _, p := range as.Policies() {
		for _, n := range as.Assets(p) {
			if q := as.Asset(p, n); q != nil {
				held[tokenKey{hex.EncodeToString(p[:]), hex.EncodeToString(n)}] += q.Int64()
			}
		}
	}
}
