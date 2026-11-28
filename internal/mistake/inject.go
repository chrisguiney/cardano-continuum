package mistake

import (
	"bytes"
	"errors"
	"fmt"
	"math/big"

	"github.com/blinklabs-io/gouroboros/cbor"
	"github.com/blinklabs-io/gouroboros/ledger/common"
	"github.com/blinklabs-io/gouroboros/ledger/conway"
	"github.com/blinklabs-io/gouroboros/ledger/shelley"

	"github.com/chrisguiney/cardano-continuum/internal/build"
	"github.com/chrisguiney/cardano-continuum/internal/keys"
)

// ErrNotApplicable reports that a mistake cannot be made on this
// transaction: a script mistake on a payment, a collateral mistake without a
// collateral return, an edit that needs a change output when there is none.
var ErrNotApplicable = errors.New("mistake: not applicable to this transaction")

// ErrNoSigner reports that a key witness of the transaction has no signer in
// Ctx, so the edited body cannot be re-signed.
var ErrNoSigner = errors.New("mistake: no signer for a witness")

// Ctx is what Inject needs beyond the transaction.
type Ctx struct {
	// Params are the builder's protocol parameters: the fee formula, the
	// collateral percentage and the PlutusV3 cost model for the script
	// data hash.
	Params build.Params
	// Signers hold a key for every key witness, to re-sign an edited body.
	Signers []keys.Signer
	// Change is the address the builder sent change to. Edits that move
	// value take it from or give it to the last output paying Change.
	Change common.Address
	// Slot is a slot the chain has already reached; Expired makes it the
	// transaction's upper validity bound.
	Slot uint64
	// Stale is an output the actor spent before. Without one, StaleInput
	// spends an output that never existed, which the ledger answers the
	// same way.
	Stale *shelley.ShelleyTransactionInput
}

// Body and witness set keys (Conway CDDL, cardano-ledger
// eras/conway/impl/cddl-files/conway.cddl).
const (
	keyInputs      = 0
	keyOutputs     = 1
	keyFee         = 2
	keyTTL         = 3
	keyDataHash    = 11
	keyCollateral  = 13
	keyCollReturn  = 16
	keyTotalColl   = 17
	witVKeys       = 0
	witPlutusV1    = 3
	witDatums      = 4
	witRedeemers   = 5
	witPlutusV2    = 6
	witPlutusV3    = 7
	wrongRedeemer  = 42
	exUnitsDivisor = 10
)

// structural are the mistakes that edit the transaction before the fee is
// settled, in the order they are made.
var structural = []Kind{StaleInput, OutputTooSmall, Expired, WrongNetwork, WrongAddress, ExUnitsLow, WrongDatum,
	ExUnitsOverDeclared, ExUnitsUnderDeclared}

// budgetEdits are the mistakes that edit redeemers, so the script data
// hash is recomputed.
var budgetEdits = Of(ExUnitsLow, WrongDatum, ExUnitsOverDeclared, ExUnitsUnderDeclared)

// Inject makes the mistakes ks in tx, a correct Conway transaction from
// internal/build, and returns the mutant with the verdict a node must give
// its first send. Every edit but the mistakes' own keeps the transaction
// valid: value moved to or from the change output, the fee raised to the
// new size's minimum, the script data hash recomputed over edited
// redeemers, and the body re-signed (see Resigns). DoubleSubmit and
// WrongAddress leave a transaction the node accepts; DoubleSubmit's bytes
// are tx itself, and the caller sends them twice, judging the second send
// with Expect(ks, true). Inject is pure.
func Inject(tx []byte, ks KindSet, c Ctx) ([]byte, Expected, error) {
	ks &= Of(Kinds()...)
	want := ExpectOn(ks, false, c.Params)
	m, err := parse(tx, c)
	if err != nil {
		return nil, Expected{}, err
	}
	for _, k := range structural {
		if !ks.Has(k) {
			continue
		}
		if err := m.make(k); err != nil {
			return nil, Expected{}, fmt.Errorf("mistake: %v: %w", k, err)
		}
	}
	if ks&budgetEdits != 0 {
		if err := m.rehash(); err != nil {
			return nil, Expected{}, err
		}
	}
	if err := m.settle(ks.Has(FeeTooSmall), ks.Has(CollateralShort), ks.Has(ExUnitsOverDeclared)); err != nil {
		return nil, Expected{}, err
	}
	if !bytes.Equal(m.body().join(), m.orig) {
		if err := m.resign(); err != nil {
			return nil, Expected{}, err
		}
	}
	if ks.Has(MissingSigner) {
		if err := m.dropSigner(); err != nil {
			return nil, Expected{}, fmt.Errorf("mistake: %v: %w", MissingSigner, err)
		}
	}
	if ks.Has(WrongAddress) {
		if want.Lost, err = m.outs[0].coin(); err != nil {
			return nil, Expected{}, err
		}
	}
	return m.encode(), want, nil
}

// RunsScript reports whether tx carries redeemers, so a node runs a Plutus
// script for it. Bytes that are not a transaction run none.
func RunsScript(tx []byte) bool {
	parts, err := splitArray(tx)
	if err != nil || len(parts) != 4 {
		return false
	}
	wits, err := splitFields(parts[1])
	if err != nil {
		return false
	}
	_, ok := wits.get(witRedeemers)

	return ok
}

// Applicable returns the mistakes Inject can make on tx alone.
func Applicable(tx []byte, c Ctx) KindSet {
	var s KindSet
	for _, k := range Kinds() {
		if _, _, err := Inject(tx, Of(k), c); err == nil {
			s |= Of(k)
		}
	}
	return s
}

// mutant is a transaction being edited.
type mutant struct {
	c      Ctx
	fs     fields // the body less its outputs, which are in outs
	outs   []output
	wits   fields
	rest   []cbor.RawMessage // is_valid and auxiliary data, untouched
	change int               // index of the change output, -1 if none
	orig   []byte            // the body as built
	owits  fields            // the witness set as built
}

func parse(tx []byte, c Ctx) (*mutant, error) {
	parts, err := splitArray(tx)
	if err != nil {
		return nil, err
	}
	if len(parts) != 4 {
		return nil, fmt.Errorf("%w: transaction of %d parts", errShape, len(parts))
	}
	m := &mutant{c: c, rest: parts[2:], change: -1, orig: parts[0]}
	if m.fs, err = splitFields(parts[0]); err != nil {
		return nil, err
	}
	if m.wits, err = splitFields(parts[1]); err != nil {
		return nil, err
	}
	m.owits = append(fields(nil), m.wits...)
	raw, ok := m.fs.get(keyOutputs)
	if !ok {
		return nil, fmt.Errorf("%w: no outputs", errShape)
	}
	outs, err := splitArray(raw)
	if err != nil {
		return nil, err
	}
	if len(outs) == 0 {
		return nil, fmt.Errorf("%w: no outputs", errShape)
	}
	changeAddr, err := c.Change.Bytes()
	if err != nil {
		changeAddr = nil
	}
	for i, raw := range outs {
		o, err := splitOutput(raw)
		if err != nil {
			return nil, err
		}
		m.outs = append(m.outs, o)
		if a, err := o.address(); err == nil && changeAddr != nil && bytes.Equal(a, changeAddr) {
			m.change = i
		}
	}
	return m, nil
}

func (m *mutant) body() fields {
	outs := make([]cbor.RawMessage, len(m.outs))
	for i, o := range m.outs {
		outs[i] = o.join()
	}
	return m.fs.set(keyOutputs, joinArray(outs))
}

func (m *mutant) encode() []byte {
	return joinArray([]cbor.RawMessage{m.body().join(), m.wits.join(), m.rest[0], m.rest[1]})
}

func (m *mutant) make(k Kind) error {
	switch k {
	case StaleInput:
		return m.stale()
	case OutputTooSmall:
		return m.dust()
	case Expired:
		m.fs = m.fs.set(keyTTL, encUint(m.c.Slot))
		return nil
	case WrongNetwork:
		return m.readdress(networkTarget(m.wits, m.change), func(a []byte) { a[0] ^= 1 })
	case WrongAddress:
		if _, ok := m.wits.get(witRedeemers); ok {
			return fmt.Errorf("%w: a script spend validates its outputs", ErrNotApplicable)
		}
		return m.readdress(0, func(a []byte) {
			h := common.Blake2b224Hash(a[1:29])
			copy(a[1:29], h[:])
		})
	case ExUnitsLow:
		return m.redeemers(func(r *redeemer) {
			r.mem /= exUnitsDivisor
			r.steps /= exUnitsDivisor
		})
	case WrongDatum:
		return m.redeemers(func(r *redeemer) { r.data = encUint(wrongRedeemer) })
	case ExUnitsOverDeclared:
		return overDeclare(m)
	case ExUnitsUnderDeclared:
		return underDeclare(m)
	}
	return fmt.Errorf("mistake: %v is not structural", k)
}

// stale adds an input the ledger no longer holds.
func (m *mutant) stale() error {
	raw, ok := m.fs.get(keyInputs)
	if !ok {
		return fmt.Errorf("%w: no inputs", errShape)
	}
	ins, err := splitSet(raw)
	if err != nil {
		return err
	}
	ref := shelley.ShelleyTransactionInput{TxId: common.Blake2b256Hash(m.orig), OutputIndex: 0}
	if m.c.Stale != nil {
		ref = *m.c.Stale
	}
	in := joinArray([]cbor.RawMessage{encBytes(ref.TxId[:]), encUint(uint64(ref.OutputIndex))})
	for _, x := range ins.items {
		if bytes.Equal(x, in) {
			return fmt.Errorf("%w: the stale input is already spent here", ErrNotApplicable)
		}
	}
	ins.items = append(ins.items, in)
	m.fs = m.fs.set(keyInputs, ins.join())
	return nil
}

// dust adds an output of one lovelace to the change address, taken from the
// change.
func (m *mutant) dust() error {
	if err := m.moveChange(-1); err != nil {
		return err
	}
	addr, err := m.outs[m.change].address()
	if err != nil {
		return err
	}
	m.outs = append(m.outs, output{m: fields{{0, encBytes(addr)}, {1, encUint(1)}}})
	return nil
}

// networkTarget is the output wrong_network edits. A script spend's
// validator may read any output it pays (the disbursement compares a whole
// address), so there the edit goes to the change output, which no validator
// reads; a failed script would add ValidationTagMismatch to the class the
// mistake is meant to cause.
func networkTarget(wits fields, change int) int {
	if _, ok := wits.get(witRedeemers); ok {
		return change
	}

	return 0
}

// readdress edits output i's Shelley address in place.
func (m *mutant) readdress(i int, edit func(a []byte)) error {
	if i < 0 {
		return fmt.Errorf("%w: no change output to readdress", ErrNotApplicable)
	}

	a, err := m.outs[i].address()
	if err != nil {
		return err
	}
	// Types 0-7 are payment addresses: a header, a 28-byte payment
	// credential, then the delegation part (CIP-19).
	if len(a) < 29 || a[0]>>4 > 7 {
		return fmt.Errorf("%w: output %d is not a Shelley payment address", ErrNotApplicable, i)
	}
	a = bytes.Clone(a)
	edit(a)
	m.outs[i] = m.outs[i].with(0, encBytes(a))
	return nil
}

// moveChange adds delta lovelace to the change output.
func (m *mutant) moveChange(delta int64) error {
	if m.change < 0 {
		return fmt.Errorf("%w: no change output", ErrNotApplicable)
	}
	c, err := m.outs[m.change].coin()
	if err != nil {
		return err
	}
	if delta < 0 && uint64(-delta) > c {
		return fmt.Errorf("%w: change %d cannot cover %d", ErrNotApplicable, c, -delta)
	}
	m.outs[m.change] = m.outs[m.change].withCoin(uint64(int64(c) + delta))
	return nil
}

func (m *mutant) fee() (uint64, error) {
	raw, ok := m.fs.get(keyFee)
	if !ok {
		return 0, fmt.Errorf("%w: no fee", errShape)
	}
	return getUint(raw)
}

// settle makes the fee match the edited transaction: at least the minimum
// for its size, or below half of it when under is set, the difference
// moved to or from the change. With short set it also declares less
// collateral than the settled fee needs; with cover, at least as much as it
// needs, for an edit that raises the script fee. Each pass can change the size, so
// it repeats until the encoding is stable; under only lowers the fee and
// otherwise it only rises, so the passes converge.
func (m *mutant) settle(under, short, cover bool) error {
	coll, err := m.collateral(short || cover)
	if err != nil {
		return err
	}
	const passes = 6
	for range passes {
		before := m.encode()
		min, err := m.minFee(before)
		if err != nil {
			return err
		}
		fee, err := m.fee()
		if err != nil {
			return err
		}
		target := fee
		switch {
		case under && fee*2 > min:
			target = min / 2
		case !under && fee < min:
			target = min
		}
		if target != fee {
			if err := m.moveChange(int64(fee) - int64(target)); err != nil {
				return err
			}
			m.fs = m.fs.set(keyFee, encUint(target))
		}
		if short {
			if err := coll.shorten(m, target); err != nil {
				return err
			}
		}
		if cover {
			if err := coll.cover(m, target); err != nil {
				return err
			}
		}
		if bytes.Equal(m.encode(), before) {
			return nil
		}
	}
	return fmt.Errorf("mistake: fee did not settle in %d passes", passes)
}

// collateral is the declared collateral as built: total_collateral and the
// collateral return. Their difference to the collateral inputs is the
// balance, which shorten keeps equal to the declared total.
type collateral struct{ total, ret uint64 }

func (m *mutant) collateral(short bool) (collateral, error) {
	if !short {
		return collateral{}, nil
	}
	_, hasIns := m.fs.get(keyCollateral)
	tot, hasTot := m.fs.get(keyTotalColl)
	ret, hasRet := m.fs.get(keyCollReturn)
	if !hasIns || !hasTot || !hasRet {
		return collateral{}, fmt.Errorf("%w: no collateral, total collateral or collateral return", ErrNotApplicable)
	}
	total, err := getUint(tot)
	if err != nil {
		return collateral{}, err
	}
	o, err := splitOutput(ret)
	if err != nil {
		return collateral{}, err
	}
	coin, err := o.coin()
	if err != nil {
		return collateral{}, err
	}
	return collateral{total: total, ret: coin}, nil
}

// shorten declares one lovelace less collateral than fee requires
// (ceiling(fee * collateralPercent / 100)) and returns the rest.
func (c collateral) shorten(m *mutant, fee uint64) error {
	need := (fee*uint64(m.c.Params.CollateralPercent) + 99) / 100
	if need == 0 || need-1 >= c.total {
		return fmt.Errorf("%w: collateral %d already short of %d", ErrNotApplicable, c.total, need)
	}
	short := need - 1
	raw, _ := m.fs.get(keyCollReturn)
	o, err := splitOutput(raw)
	if err != nil {
		return err
	}
	m.fs = m.fs.set(keyTotalColl, encUint(short))
	m.fs = m.fs.set(keyCollReturn, o.withCoin(c.ret+c.total-short).join())
	return nil
}

// minReturn is the least lovelace cover leaves in the collateral return,
// above min-UTxO for a plain ADA output.
const minReturn = 1_000_000

// cover declares at least the collateral fee requires, taken from the
// collateral return.
func (c collateral) cover(m *mutant, fee uint64) error {
	need := (fee*uint64(m.c.Params.CollateralPercent) + 99) / 100
	if need <= c.total {
		return nil
	}
	if c.ret+c.total < need+minReturn {
		return fmt.Errorf("%w: collateral return %d cannot cover %d", ErrNotApplicable, c.ret, need-c.total)
	}
	raw, _ := m.fs.get(keyCollReturn)
	o, err := splitOutput(raw)
	if err != nil {
		return err
	}
	m.fs = m.fs.set(keyTotalColl, encUint(need))
	m.fs = m.fs.set(keyCollReturn, o.withCoin(c.ret+c.total-need).join())
	return nil
}

// minFee is the ledger's minimum fee for raw: a*TxSize + b plus
// ceiling(memPrice*mem + stepPrice*steps) over the redeemers (cardano-ledger
// Conway getMinFeeTx without reference scripts), as internal/build pays.
func (m *mutant) minFee(raw []byte) (uint64, error) {
	tx, err := conway.NewConwayTransactionFromCbor(raw)
	if err != nil {
		return 0, fmt.Errorf("mistake: decode: %w", err)
	}
	size, err := common.TxSize(tx)
	if err != nil {
		return 0, fmt.Errorf("mistake: size: %w", err)
	}
	p := m.c.Params
	fee := p.MinFeeA*uint64(size) + p.MinFeeB
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
	if p.Prices.MemPrice == nil || p.Prices.StepPrice == nil {
		return 0, errors.New("mistake: execution unit prices unset")
	}
	cost := new(big.Rat).Mul(p.Prices.MemPrice.Rat, big.NewRat(mem, 1))
	cost.Add(cost, new(big.Rat).Mul(p.Prices.StepPrice.Rat, big.NewRat(steps, 1)))
	ceil := new(big.Int).Add(cost.Num(), new(big.Int).Sub(cost.Denom(), big.NewInt(1)))
	return fee + ceil.Quo(ceil, cost.Denom()).Uint64(), nil
}

// redeemers applies edit to every redeemer, in either Conway form: the map
// {[tag, index]: [data, ex_units]} or the legacy array
// [[tag, index, data, ex_units]].
func (m *mutant) redeemers(edit func(*redeemer)) error {
	raw, ok := m.wits.get(witRedeemers)
	if !ok {
		return fmt.Errorf("%w: no redeemers", ErrNotApplicable)
	}
	major, _, _, err := head(raw)
	if err != nil {
		return err
	}
	if major == majorMap {
		kv, err := container(raw, majorMap)
		if err != nil {
			return err
		}
		if len(kv) == 0 {
			return fmt.Errorf("%w: no redeemers", ErrNotApplicable)
		}
		for i := 1; i < len(kv); i += 2 {
			if kv[i], err = editPair(kv[i], edit); err != nil {
				return err
			}
		}
		out := writeHead(majorMap, uint64(len(kv)/2))
		for _, x := range kv {
			out = append(out, x...)
		}
		m.wits = m.wits.set(witRedeemers, out)
		return nil
	}
	rs, err := splitArray(raw)
	if err != nil {
		return err
	}
	if len(rs) == 0 {
		return fmt.Errorf("%w: no redeemers", ErrNotApplicable)
	}
	for i, r := range rs {
		xs, err := splitArray(r)
		if err != nil || len(xs) != 4 {
			return fmt.Errorf("%w: redeemer", errShape)
		}
		pair, err := editPair(joinArray(xs[2:]), edit)
		if err != nil {
			return err
		}
		tail, err := splitArray(pair)
		if err != nil {
			return err
		}
		rs[i] = joinArray(append(xs[:2:2], tail...))
	}
	m.wits = m.wits.set(witRedeemers, joinArray(rs))
	return nil
}

// scriptDataHash is blake2b-256 over the redeemers, the datums when there
// are any, and the language views of the Plutus versions the witnesses
// carry (cardano-ledger Alonzo hashScriptIntegrity; gouroboros
// ledger/conway/rules.go UtxoValidateScriptDataHash). Only PlutusV3 has a
// cost model in build.Params.
func (m *mutant) scriptDataHash(wits fields) (common.Blake2b256, error) {
	if _, ok := wits.get(witPlutusV1); ok {
		return common.Blake2b256{}, fmt.Errorf("%w: PlutusV1 has no cost model here", ErrNotApplicable)
	}
	if _, ok := wits.get(witPlutusV2); ok {
		return common.Blake2b256{}, fmt.Errorf("%w: PlutusV2 has no cost model here", ErrNotApplicable)
	}
	used := map[uint]struct{}{}
	if _, ok := wits.get(witPlutusV3); ok {
		used[2] = struct{}{}
	}
	views, err := common.EncodeLangViews(used, map[uint][]int64{2: m.c.Params.PlutusV3})
	if err != nil {
		return common.Blake2b256{}, fmt.Errorf("mistake: language views: %w", err)
	}
	red, _ := wits.get(witRedeemers)
	in := bytes.Clone(red)
	if d, ok := wits.get(witDatums); ok {
		in = append(in, d...)
	}
	return common.Blake2b256Hash(append(in, views...)), nil
}

// rehash recomputes the script data hash over edited redeemers. It first
// recomputes the builder's own hash: if that does not reproduce, the inputs
// to the hash are not what this package assumes, and editing would make a
// PPViewHashesDontMatch the mistake never meant.
func (m *mutant) rehash() error {
	raw, ok := m.fs.get(keyDataHash)
	if !ok {
		return fmt.Errorf("%w: no script data hash", ErrNotApplicable)
	}
	declared, err := getBytes(raw)
	if err != nil {
		return err
	}
	orig, err := m.scriptDataHash(m.owits)
	if err != nil {
		return err
	}
	if !bytes.Equal(orig[:], declared) {
		return fmt.Errorf("%w: the built script data hash does not reproduce", ErrNotApplicable)
	}
	h, err := m.scriptDataHash(m.wits)
	if err != nil {
		return err
	}
	m.fs = m.fs.set(keyDataHash, encBytes(h[:]))
	return nil
}

// resign replaces every key witness with a signature over the edited body.
func (m *mutant) resign() error {
	raw, ok := m.wits.get(witVKeys)
	if !ok {
		return nil
	}
	ws, err := splitSet(raw)
	if err != nil {
		return err
	}
	body := m.body().join()
	for i, w := range ws.items {
		xs, err := splitArray(w)
		if err != nil || len(xs) != 2 {
			return fmt.Errorf("%w: vkey witness", errShape)
		}
		vkey, err := getBytes(xs[0])
		if err != nil {
			return err
		}
		found := false
		for _, s := range m.c.Signers {
			if bytes.Equal(s.VKey(), vkey) {
				sig := s.SignTxBody(body)
				ws.items[i] = joinArray([]cbor.RawMessage{encBytes(sig.VKey), encBytes(sig.Signature)})
				found = true
				break
			}
		}
		if !found {
			h := common.Blake2b224Hash(vkey)
			return fmt.Errorf("%w: key %x", ErrNoSigner, h[:4])
		}
	}
	m.wits = m.wits.set(witVKeys, ws.join())
	return nil
}

// dropSigner removes the first key witness. Every key witness the builder
// adds is required, so the ledger reports it missing.
func (m *mutant) dropSigner() error {
	raw, ok := m.wits.get(witVKeys)
	if !ok {
		return fmt.Errorf("%w: no key witness", ErrNotApplicable)
	}
	ws, err := splitSet(raw)
	if err != nil {
		return err
	}
	if len(ws.items) == 0 {
		return fmt.Errorf("%w: no key witness", ErrNotApplicable)
	}
	ws.items = ws.items[1:]
	if len(ws.items) == 0 {
		m.wits = m.wits.del(witVKeys)
		return nil
	}
	m.wits = m.wits.set(witVKeys, ws.join())
	return nil
}
