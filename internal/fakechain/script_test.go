package fakechain

import (
	"errors"
	"slices"
	"testing"

	"github.com/blinklabs-io/gouroboros/cbor"
	"github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/plutigo/syn"

	"github.com/chrisguiney/cardano-continuum/internal/genesis"
	"github.com/chrisguiney/cardano-continuum/internal/keys"
)

// Always-succeeding minting policies. A PlutusV2 policy takes the redeemer
// and the context; a PlutusV3 one takes the context alone and must return
// unit.
const (
	alwaysV2 = "(program 1.0.0 (lam r (lam ctx (con unit ()))))"
	alwaysV3 = "(program 1.1.0 (lam ctx (con unit ())))"
)

// Witness-set keys of the script scripts (Conway CDDL).
const (
	witV2 = 6
	witV3 = 7
)

// policy is a compiled minting policy: its witness bytes, hash and
// language.
type policy struct {
	script []byte
	hash   common.Blake2b224
	lang   genesis.Language
}

func compilePolicy(t *testing.T, src string, lang genesis.Language) policy {
	t.Helper()
	prog, err := syn.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	db, err := syn.NameToDeBruijn(prog)
	if err != nil {
		t.Fatal(err)
	}
	flat, err := syn.Encode(db)
	if err != nil {
		t.Fatal(err)
	}
	wrapped, err := cbor.Encode(flat)
	if err != nil {
		t.Fatal(err)
	}
	p := policy{script: wrapped, lang: lang}
	switch lang {
	case genesis.PlutusV2:
		p.hash = common.PlutusV2Script(wrapped).Hash()
	default:
		p.hash = common.PlutusV3Script(wrapped).Hash()
	}
	return p
}

// mintTx mints one token under p from w's genesis output, declaring units
// for the policy's redeemer. The genesis output is also the collateral.
func (f *fixture) mintTx(p policy, w keys.Wallet, units common.ExUnits, ttl uint64) []byte {
	t := f.t
	t.Helper()
	in := f.genesisRef(w)
	ins := []any{[]any{in.TxID[:], in.Index}}
	addr, err := w.Address().Bytes()
	if err != nil {
		t.Fatal(err)
	}
	asset := map[any]any{cbor.NewByteString(p.hash.Bytes()): map[any]any{cbor.NewByteString([]byte("tok")): uint64(1)}}
	redeemers, err := cbor.Encode([]any{[]any{uint64(1), uint64(0), uint64(0), []any{uint64(units.Memory), uint64(units.Steps)}}})
	if err != nil {
		t.Fatal(err)
	}
	pp := DefaultParams()
	views, err := common.EncodeLangViews(map[uint]struct{}{uint(p.lang): {}}, pp.CostModels)
	if err != nil {
		t.Fatal(err)
	}
	dataHash := common.Blake2b256Hash(append(slices.Clone(redeemers), views...))
	encode := func(fee uint64) []byte {
		body := map[uint]any{
			0: ins, 1: []any{map[uint]any{0: addr, 1: []any{testLovelace - fee, asset}}}, 2: fee,
			9: asset, 11: dataHash[:], 13: ins,
		}
		if ttl != 0 {
			body[3] = ttl
		}
		raw, err := cbor.Encode(body)
		if err != nil {
			t.Fatal(err)
		}
		sig := w.SignTxBody(raw)
		key := uint(witV3)
		if p.lang == genesis.PlutusV2 {
			key = witV2
		}
		ws := map[uint]any{0: []any{[]any{sig.VKey, sig.Signature}}, 5: cbor.RawMessage(redeemers), key: []any{p.script}}
		tx, err := cbor.Encode([]any{cbor.RawMessage(raw), ws, true, nil})
		if err != nil {
			t.Fatal(err)
		}
		return tx
	}
	return encode(minFee(t, encode(feePlaceholder)))
}

// TestPlutusV2Accepted: with the genesis cost models in place, a PlutusV2
// script runs on the fake chain and its transaction is forged.
func TestPlutusV2Accepted(t *testing.T) {
	f := newFixture(t, nil)
	p := compilePolicy(t, alwaysV2, genesis.PlutusV2)
	id := f.mustSubmit(f.mintTx(p, f.alice, common.ExUnits{Memory: 100_000, Steps: 100_000_000}, 0))
	f.advance(2)
	if !f.forged(id) {
		t.Fatal("the PlutusV2 mint was not forged")
	}
}

// TestDefaultParamsFromGenesis: the fake chain's limits and cost models
// are the devnet genesis's.
func TestDefaultParamsFromGenesis(t *testing.T) {
	pp := DefaultParams()
	s := genesis.Default()
	if pp.MaxTxExUnits.Memory != int64(s.TxExUnits.Memory) || pp.MaxTxExUnits.Steps != int64(s.TxExUnits.Steps) ||
		pp.MaxBlockExUnits.Memory != int64(s.BlockExUnits.Memory) || pp.MaxBlockExUnits.Steps != int64(s.BlockExUnits.Steps) {
		t.Fatalf("limits tx %+v block %+v, genesis %+v %+v", pp.MaxTxExUnits, pp.MaxBlockExUnits, s.TxExUnits, s.BlockExUnits)
	}
	want, err := genesis.CostModels(s)
	if err != nil {
		t.Fatal(err)
	}
	for k, m := range want {
		if !slices.Equal(pp.CostModels[k], m) {
			t.Errorf("cost model %d: %d entries, genesis %d", k, len(pp.CostModels[k]), len(m))
		}
	}
}

// gapParams are a devnet whose per-transaction memory exceeds the
// per-block memory.
func gapParams(t *testing.T) func(*Config) {
	return func(c *Config) {
		s := genesis.Default()
		s.TxExUnits.Memory, s.Allow.InconsistentExUnits = 140_000_000, true
		pp, err := ParamsFor(s)
		if err != nil {
			t.Fatal(err)
		}
		c.Params = &pp
	}
}

// TestBlockBudgetHeadOfLine: a transaction within the per-transaction
// budget but over the per-block one is admitted and never forged. Like the
// node's forging, which takes the longest mempool prefix within the block
// capacity (ouroboros-consensus Mempool/Impl/Common.hs, splitAfterTxSize),
// the fake stops at it, so the payment behind it waits until it expires.
func TestBlockBudgetHeadOfLine(t *testing.T) {
	f := newFixture(t, gapParams(t))
	p := compilePolicy(t, alwaysV3, genesis.PlutusV3)
	gap := f.mustSubmit(f.mintTx(p, f.alice, common.ExUnits{Memory: 100_000_000, Steps: 1_000_000_000}, 10))
	pay := f.mustSubmit(f.pay(f.bob, f.alice.Address(), 5_000_000).build(t))
	f.advance(8)
	if f.forged(gap) || f.forged(pay) {
		t.Fatalf("forged behind an over-budget head: gap %v pay %v", f.forged(gap), f.forged(pay))
	}
	f.advance(14)
	if f.forged(gap) {
		t.Fatal("the over-budget transaction was forged")
	}
	if !f.forged(pay) {
		t.Fatal("the payment was not forged after the over-budget transaction expired")
	}
	if !slices.ContainsFunc(f.chain.Evictions(), func(e Eviction) bool { return e.Tx == gap }) {
		t.Fatal("the over-budget transaction was never evicted")
	}
}

// TestBlockBudgetPacksPrefix: transactions within the block budget
// together are forged in one block.
func TestBlockBudgetPacksPrefix(t *testing.T) {
	f := newFixture(t, nil)
	p := compilePolicy(t, alwaysV3, genesis.PlutusV3)
	units := common.ExUnits{Memory: 10_000_000, Steps: 1_000_000_000}
	a := f.mustSubmit(f.mintTx(p, f.alice, units, 0))
	b := f.mustSubmit(f.mintTx(p, f.bob, units, 0))
	f.advance(2)
	if !f.forged(a) || !f.forged(b) {
		t.Fatal("two transactions within the block budget were not both forged")
	}
}

// TestOverfill: the Overfill fault forges a block past its execution
// budget, as a faulty forger would, for the oracle to catch.
func TestOverfill(t *testing.T) {
	f := newFixture(t, gapParams(t))
	p := compilePolicy(t, alwaysV3, genesis.PlutusV3)
	gap := f.mustSubmit(f.mintTx(p, f.alice, common.ExUnits{Memory: 100_000_000, Steps: 1_000_000_000}, 0))
	if err := f.chain.Inject(Overfill{}); err != nil {
		t.Fatal(err)
	}
	f.advance(2)
	if !f.forged(gap) {
		t.Fatal("Overfill did not forge the over-budget transaction")
	}
}

func TestPhase2InvalidStillUnsupported(t *testing.T) {
	f := newFixture(t, nil)
	p := compilePolicy(t, alwaysV3, genesis.PlutusV3)
	tx := f.mintTx(p, f.alice, common.ExUnits{Memory: 100_000, Steps: 100_000_000}, 0)
	var parts []cbor.RawMessage
	if _, err := cbor.Decode(tx, &parts); err != nil {
		t.Fatal(err)
	}
	parts[2] = cbor.RawMessage{0xf4}
	bad, err := cbor.Encode(parts)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.submit(bad); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("err = %v, want ErrUnsupported", err)
	}
}

// forged reports whether a block holds id.
func (f *fixture) forged(id TxID) bool {
	for _, b := range f.chain.Blocks() {
		if slices.Contains(b.Txs, id) {
			return true
		}
	}
	return false
}
