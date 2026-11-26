package mistake

import (
	"bytes"
	"fmt"

	"github.com/blinklabs-io/gouroboros/cbor"
	"github.com/blinklabs-io/gouroboros/ledger/common"
)

// redeemer is one redeemer's editable parts.
type redeemer struct {
	data       cbor.RawMessage
	mem, steps uint64
}

func splitExUnits(raw []byte) (mem, steps uint64, err error) {
	xs, err := splitArray(raw)
	if err != nil || len(xs) != 2 {
		return 0, 0, fmt.Errorf("%w: execution units", errShape)
	}
	if mem, err = getUint(xs[0]); err != nil {
		return 0, 0, err
	}
	steps, err = getUint(xs[1])
	return mem, steps, err
}

func joinExUnits(mem, steps uint64) cbor.RawMessage {
	return joinArray([]cbor.RawMessage{encUint(mem), encUint(steps)})
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

// editPair edits one [data, ex_units].
func editPair(raw []byte, edit func(*redeemer)) (cbor.RawMessage, error) {
	xs, err := splitArray(raw)
	if err != nil || len(xs) != 2 {
		return nil, fmt.Errorf("%w: redeemer value", errShape)
	}
	r := redeemer{data: xs[0]}
	if r.mem, r.steps, err = splitExUnits(xs[1]); err != nil {
		return nil, err
	}
	edit(&r)
	return joinArray([]cbor.RawMessage{r.data, joinExUnits(r.mem, r.steps)}), nil
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
