package scenario

import (
	"fmt"
	"math/big"

	"github.com/blinklabs-io/gouroboros/cbor"
	"github.com/blinklabs-io/gouroboros/ledger/babbage"
	"github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/gouroboros/ledger/conway"

	"github.com/chrisguiney/cardano-continuum/internal/keys"
)

// targetEra and targetMajor are the era and protocol version every adapter
// here writes for.
const (
	targetEra   = "Conway"
	targetMajor = 10
)

// Reserved actors. The funder holds the genesis output a scenario's
// precondition transaction spends; the scratch actor owns the model's
// key-locked outputs and its one stake credential; the second actor is the
// model's other key (Witnesses.tla's v2).
const (
	scratchActor keys.ActorID = 1
	secondActor  keys.ActorID = 2
)

// world is the concrete material a scenario is built from.
type world struct {
	net     uint8
	funder  keys.Wallet
	scratch keys.Wallet
	second  keys.Wallet
	// policy is the hash of anyoneScript: the model token's policy id and
	// the payment credential of a script-locked output.
	policy common.Blake2b224
}

func newWorld(env Env) (world, error) {
	store, err := keys.New(env.Seed.Derive("scenario", "keys"), env.NetworkID)
	if err != nil {
		return world{}, fmt.Errorf("scenario keys: %w", err)
	}
	return world{
		net: env.NetworkID, funder: store.Faucet(),
		scratch: store.Wallet(scratchActor), second: store.Wallet(secondActor),
		policy: nativeHash(anyoneScript),
	}, nil
}

// tokenName is the asset name of the model's one token.
var tokenName = []byte("t")

func (w world) address(kind uint8, cred []byte) []byte {
	a, err := common.NewAddressFromParts(kind, w.net, cred, nil)
	if err != nil {
		// Unreachable: cred is a 28-byte hash and the network id was
		// checked by keys.New.
		panic("scenario: address: " + err.Error())
	}
	b, err := a.Bytes()
	if err != nil {
		panic("scenario: address bytes: " + err.Error())
	}
	return b
}

// keyAddr is the enterprise address of a payment key hash.
func (w world) keyAddr(h keys.Credential) []byte {
	return w.address(common.AddressTypeKeyNone, h[:])
}

// scriptAddr is the enterprise address locked by a script hash.
func (w world) scriptAddr(h common.Blake2b224) []byte {
	return w.address(common.AddressTypeScriptNone, h[:])
}

// lockAddr is the address of the model's lock: "key" is the scratch actor's
// payment key, "script" is anyoneScript.
func (w world) lockAddr(lock string) []byte {
	if lock == "script" {
		return w.scriptAddr(w.policy)
	}
	return w.keyAddr(w.scratch.PaymentKeyHash())
}

// rewardAddr is the scratch actor's reward account.
func (w world) rewardAddr() []byte {
	b, err := w.scratch.StakeAddress().Bytes()
	if err != nil {
		panic("scenario: reward address: " + err.Error())
	}
	return b
}

// token is n of the model's token, or nil for none.
func (w world) token(n int) *asset {
	if n <= 0 {
		return nil
	}
	return &asset{Policy: w.policy, Name: tokenName, Qty: int64(n)}
}

// genesisRef is the output a fake chain or a genesis file gives the funder:
// Shelley genesis names an initial fund by the hash of its address.
func (w world) genesisRef() txIn {
	b, err := w.funder.Address().Bytes()
	if err != nil {
		panic("scenario: funder address: " + err.Error())
	}
	return txIn{ID: common.Blake2b256Hash(b)}
}

// minCoin is the least lovelace the ledger lets o hold under pp.
func minCoin(o txOut, pp *conway.ConwayProtocolParameters) (uint64, error) {
	out, err := babbage.NewBabbageTransactionOutputFromCbor(o.encode())
	if err != nil {
		return 0, fmt.Errorf("decode crafted output: %w", err)
	}
	return conway.MinCoinTxOut(out, pp)
}

// probeCoin stands in for an output's lovelace while its minimum is worked
// out: the minimum depends on the encoded size, and every amount a scenario
// writes takes the same five bytes as this one.
const probeCoin = 1_000_000

// unitFor returns the lovelace one model coin stands for: the largest
// minimum among the output shapes the scenario writes. A model output of one
// coin then sits exactly on the ledger's minimum when it has that shape, and
// at or above it otherwise.
func unitFor(shapes []txOut, pp *conway.ConwayProtocolParameters) (uint64, error) {
	var unit uint64
	for _, s := range shapes {
		s.Coin = probeCoin
		m, err := minCoin(s, pp)
		if err != nil {
			return 0, err
		}
		unit = max(unit, m)
	}
	if unit < 1<<16 || unit > 1<<28 {
		return 0, fmt.Errorf("unit %d lovelace is outside the range whose amounts encode in five bytes", unit)
	}
	return unit, nil
}

// pinParams applies the refinement mapping's parameter requirements: a
// constant minimum fee and a key deposit of exactly one unit (Utxo.tla's
// MinFee = 1 with a = 0, KeyDeposit = 1), its collateral rule (CollPct = 150,
// MaxColl = 2), and free script execution so the fee stays that constant.
func pinParams(pp conway.ConwayProtocolParameters, unit uint64) conway.ConwayProtocolParameters {
	pp.MinFeeA, pp.MinFeeB = 0, uint(unit)
	pp.KeyDeposit = uint(unit)
	pp.CollateralPercentage, pp.MaxCollateralInputs = 150, 2
	pp.ProtocolVersion.Major, pp.ProtocolVersion.Minor = targetMajor, 0
	zero := func() *cbor.Rat { return &cbor.Rat{Rat: new(big.Rat)} }
	pp.ExecutionCosts = common.ExUnitPrice{MemPrice: zero(), StepPrice: zero()}
	pp.MinFeeRefScriptCostPerByte = zero()
	return pp
}

// signers are the scratch actor's payment key and, when a certificate or a
// withdrawal needs it, its stake key.
func (w world) signers(stake bool) []keys.Signer {
	s := []keys.Signer{w.scratch.PaymentSigner()}
	if stake {
		s = append(s, w.scratch.StakeSigner())
	}
	return s
}
