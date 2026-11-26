package scenario

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/big"
	"slices"

	"github.com/chrisguiney/cardano-continuum/internal/keys"
	"github.com/chrisguiney/cardano-continuum/internal/mistake"
	"github.com/chrisguiney/cardano-continuum/internal/submit"
)

// CodecCase is one input of a codec table model: the model's name and its
// input record. The reference's verdict is not stored: the adapter derives it
// from the model's own rule, restated per model below.
type CodecCase struct {
	// Model is the module name without ".tla".
	Model string          `json:"model"`
	Input json.RawMessage `json:"input"`
}

// CodecAdapter is the refinement mapping of the cbor-codec and ledger-codec
// table models: an abstract encoding becomes that byte shape inside an
// otherwise ordinary transaction (see the package documentation).
type CodecAdapter struct{}

// NewCodecAdapter returns the codec adapter.
func NewCodecAdapter() *CodecAdapter { return &CodecAdapter{} }

// Family implements Adapter.
func (*CodecAdapter) Family() string { return FamilyCodec }

// decodedClasses are what a node may answer to a transaction the reference
// decodes: the codec models say nothing about the ledger rules, and a shape
// that decodes need not balance.
var decodedClasses = []submit.Class{
	submit.ValueNotConserved, submit.OutputTooSmall, submit.OutputTooBig,
	submit.InputSetEmpty, submit.NonDisjointRefInputs, submit.BadInputs,
	submit.FeeTooSmall, submit.MissingVKeyWitnesses,
}

// codecForm is one model's mapping: place writes the input into the
// transaction and reports whether the reference decodes it.
type codecForm struct {
	place func(input json.RawMessage, w world, t *txSpec) (refAccepts bool, err error)
}

// codecForms is the mapping per model. A model with no entry has no
// transaction-level form in Conway.
var codecForms = map[string]codecForm{
	"MultiAssetQuantity": {placeQuantity},
	"PreConwaySetTag":    {placeSetTag},
	"PlutusConstrFields": {placeConstr},
	"ValueSetMembers":    {placeSetContent},
	"TxSizeFraming":      {placeFraming},
}

func (*CodecAdapter) form(s Scenario) (codecForm, error) {
	if s.Codec == nil {
		return codecForm{}, unmappable(-1, "scenario has no codec case")
	}
	f, ok := codecForms[s.Codec.Model]
	if !ok {
		return codecForm{}, unmappable(-1, "model %s has no transaction-level form in Conway", s.Codec.Model)
	}
	return f, nil
}

// place writes the case's transaction, without inputs or amounts, and the
// reference's verdict on it.
func (a *CodecAdapter) place(s Scenario, env Env) (world, txSpec, bool, error) {
	f, err := a.form(s)
	if err != nil {
		return world{}, txSpec{}, false, err
	}
	w, err := newWorld(env)
	if err != nil {
		return world{}, txSpec{}, false, err
	}
	t := txSpec{Outputs: []txOut{{Addr: w.lockAddr("key")}}, Signers: w.signers(false)}
	accepts, err := f.place(s.Codec.Input, w, &t)
	return w, t, accepts, err
}

// build is place with the amounts filled in for env's parameters.
func (a *CodecAdapter) build(s Scenario, env Env) (world, uint64, txSpec, bool, error) {
	w, t, accepts, err := a.place(s, env)
	if err != nil {
		return world{}, 0, txSpec{}, false, err
	}
	// The unit is sized on a well-formed output: the case's own may not
	// decode, and it holds two units, which covers a small datum.
	unit, err := unitFor([]txOut{{Addr: w.lockAddr("key"), Token: w.token(1)}}, &env.Params)
	if err != nil {
		return world{}, 0, txSpec{}, false, err
	}
	t.Fee, t.Outputs[0].Coin = unit, 2*unit
	return w, unit, t, accepts, nil
}

// Applicable implements Adapter.
func (a *CodecAdapter) Applicable(s Scenario, env Env) error {
	_, _, _, err := a.place(s, env)
	return err
}

// Expected implements Adapter. A case with no form has no expectation.
func (a *CodecAdapter) Expected(s Scenario, _ int) mistake.Expected {
	_, _, accepts, err := a.place(s, Env{})
	if err != nil {
		return mistake.Expected{}
	}
	return codecExpected(accepts)
}

func codecExpected(accepts bool) mistake.Expected {
	if accepts {
		return mistake.Expected{Accept: true, Classes: sorted(decodedClasses)}
	}
	return mistake.Expected{Classes: []submit.Class{submit.Undecodable}}
}

// Preconditions implements Adapter.
func (a *CodecAdapter) Preconditions(s Scenario, env Env) (Preconditions, error) {
	w, unit, _, _, err := a.build(s, env)
	if err != nil {
		return Preconditions{}, err
	}
	return Preconditions{
		Era: targetEra, ProtocolMajor: targetMajor, Params: pinParams(env.Params, unit),
		Funder: w.funder.Address(), Funds: 4 * unit, UTxOs: 1,
	}, nil
}

// Concretize implements Adapter: one output of three units, then the
// transaction that spends it into two units and the fee.
func (a *CodecAdapter) Concretize(s Scenario, env Env) (Plan, error) {
	w, unit, t, accepts, err := a.build(s, env)
	if err != nil {
		return Plan{}, err
	}
	setup := txSpec{
		Inputs: []txIn{w.genesisRef()}, Fee: unit,
		Outputs: []txOut{{Addr: w.lockAddr("key"), Coin: 3 * unit}},
		Signers: []keys.Signer{w.funder.PaymentSigner()},
	}
	raw, id := setup.encode()
	spent := txIn{ID: id}
	t.Inputs = []txIn{spent}
	for k, v := range t.Raw {
		t.Raw[k] = spliceInput(v, spent)
	}
	tx, txID := t.encode()
	return Plan{Steps: []Step{
		{Kind: Submit, Note: "build the output to spend", Index: -1, Tx: raw, ID: id, Setup: true, Slot: env.Start, Expect: mistake.Expected{Accept: true}},
		{Kind: Wait, Note: "confirm the output", Index: -1, Slot: env.Start + 1},
		{Kind: Submit, Note: "offer the encoding", Tx: tx, ID: txID, Slot: env.Start + 2, Expect: codecExpected(accepts), Strict: !accepts},
	}}, nil
}

// inputHole stands in a raw field for the encoded input, which is known only
// once the setup transaction has an id. It is not valid CBOR on its own.
var inputHole = []byte("\xff<input>\xff")

func spliceInput(raw []byte, in txIn) []byte {
	return bytes.ReplaceAll(raw, inputHole, in.encode())
}

// quantities are MultiAssetQuantity.tla's named Values.
var quantities = map[string]*big.Int{
	"zero":         big.NewInt(0),
	"five":         big.NewInt(5),
	"w64max":       new(big.Int).SetUint64(1<<64 - 1),
	"i64maxPlus1":  new(big.Int).SetUint64(1 << 63),
	"minusOne":     big.NewInt(-1),
	"i64min":       big.NewInt(-1 << 63),
	"i64minMinus1": new(big.Int).Sub(big.NewInt(-1<<63), big.NewInt(1)),
	"beyond64":     new(big.Int).Lsh(big.NewInt(1), 64),
}

var (
	word64 = []string{"zero", "five", "w64max", "i64maxPlus1"}
	int64s = []string{"zero", "five", "minusOne", "i64min"}
)

type quantityInput struct {
	PV    int    `json:"pv"`
	Field string `json:"field"`
	Value string `json:"value"`
	Enc   string `json:"enc"`
}

// encodeQuantity writes v in the model's encoding: a major type 0 or 1 head,
// a tag 2 or 3 bignum, or that bignum with its tag in a two-byte head.
func encodeQuantity(v *big.Int, enc string) ([]byte, error) {
	mag, major, tag := new(big.Int).Set(v), byte(majUint), uint8(2)
	if v.Sign() < 0 {
		mag.Add(mag, big.NewInt(1)).Neg(mag)
		major, tag = majNint, 3
	}
	switch enc {
	case "head":
		if !mag.IsUint64() {
			return nil, unmappable(-1, "value does not fit a CBOR integer head")
		}
		return head(major, mag.Uint64()), nil
	case "bignum", "bignumBehindDup":
		return cTag(uint64(tag), cBytes(mag.Bytes())), nil
	case "bignumLongTag":
		return append(wideHead(majTag, tag), cBytes(mag.Bytes())...), nil
	}
	return nil, unmappable(-1, "unknown quantity encoding %q", enc)
}

// placeQuantity is MultiAssetQuantity.tla. RefAccepts: the reference reads
// only heads, and only values in the field's domain (InDomain); from protocol
// version 9 a zero asset quantity fails. The model's version is replaced by
// the target's, which is past 9.
func placeQuantity(raw json.RawMessage, w world, t *txSpec) (bool, error) {
	var in quantityInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return false, fmt.Errorf("decode MultiAssetQuantity input: %w", err)
	}
	v, ok := quantities[in.Value]
	if !ok {
		return false, unmappable(-1, "unknown quantity %q", in.Value)
	}
	q, err := encodeQuantity(v, in.Enc)
	if err != nil {
		return false, err
	}
	pairs := [][]byte{cBytes(tokenName), q}
	if in.Enc == "bignumBehindDup" {
		pairs = append(pairs, cBytes(tokenName), cUint(5))
	}
	assets := cMap(cBytes(w.policy[:]), cMap(pairs...))
	coin := cUint(2 * probeCoin)
	domain := word64
	switch in.Field {
	case "outputAsset":
		t.Outputs[0].Value = cArray(coin, assets)
	case "mintAsset":
		domain = int64s
		t.Raw = map[uint64][]byte{bodyMint: assets}
		t.Natives = [][]byte{anyoneScript}
	case "coinBare":
		t.Outputs[0].Value = q
	case "coinInPair":
		t.Outputs[0].Value = cArray(q, cMap(cBytes(w.policy[:]), cMap(cBytes(tokenName), cUint(5))))
	default:
		return false, unmappable(-1, "unknown field %q", in.Field)
	}
	isCoin := in.Field == "coinBare" || in.Field == "coinInPair"
	inDomain := slices.Contains(domain, in.Value) && (isCoin || in.Value != "zero")
	return in.Enc == "head" && inDomain, nil
}

type setTagInput struct {
	Era          string `json:"era"`
	Field        string `json:"field"`
	Tag          string `json:"tag"`
	Elsewhere258 bool   `json:"elsewhere258"`
}

// placeSetTag is PreConwaySetTag.tla at era Conway, whatever era the input
// names. RefDecodes: an untagged set, or one under tag 258; another tag
// never (allowTag).
func placeSetTag(raw json.RawMessage, w world, t *txSpec) (bool, error) {
	var in setTagInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return false, fmt.Errorf("decode PreConwaySetTag input: %w", err)
	}
	wrap := func(items []byte) []byte {
		switch in.Tag {
		case "258":
			return cTag(258, items)
		case "other":
			return cTag(259, items)
		}
		return items
	}
	h := w.scratch.PaymentKeyHash()
	one := wrap(cArray(inputHole))
	switch in.Field {
	case "inputs":
		t.Raw = map[uint64][]byte{bodyInputs: one}
	case "collateral":
		t.Raw = map[uint64][]byte{bodyCollateral: one}
	case "referenceInputs":
		t.Raw = map[uint64][]byte{bodyRefInputs: one}
	case "requiredSigners":
		t.Raw = map[uint64][]byte{bodySigners: wrap(cArray(cBytes(h[:])))}
	case "witness":
		t.WitWrap = wrap
	default:
		return false, unmappable(-1, "set field %q needs a pool registration, which is not crafted", in.Field)
	}
	return in.Tag != "other", nil
}

type constrInput struct {
	Family string `json:"family"`
	Alt    string `json:"alt"`
	Fields string `json:"fields"`
}

// fieldShapes are PlutusConstrFields.tla's FieldShapes as bytes.
var fieldShapes = map[string][]byte{
	"defList": {0x80}, "indefList": {0x9f, 0xff}, "uint": {0x01},
	"bytes": {0x41, 0x00}, "map": {0xa0}, "text": {0x61, 0x61},
	"taggedList": {0xd8, 0x1e, 0x80},
}

// placeConstr is PlutusConstrFields.tla: the constructor is the output's
// inline datum. RefAccepts: the alternative number under tag 102 is a uint,
// and the fields item is a definite or indefinite list (decodeListOf).
func placeConstr(raw json.RawMessage, _ world, t *txSpec) (bool, error) {
	var in constrInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return false, fmt.Errorf("decode PlutusConstrFields input: %w", err)
	}
	fields, ok := fieldShapes[in.Fields]
	if !ok {
		return false, unmappable(-1, "unknown fields shape %q", in.Fields)
	}
	alt := cUint(3)
	if in.Alt == "negint" {
		alt = cInt(-1)
	}
	var data []byte
	switch in.Family {
	case "121-127":
		data = cTag(121, fields)
	case "1280-1400":
		data = cTag(1280, fields)
	case "102":
		data = cTag(102, append(append(head(majArray, 2), alt...), fields...))
	default:
		return false, unmappable(-1, "unknown constructor family %q", in.Family)
	}
	t.Outputs[0].Datum = cArray(cUint(1), cTag(24, cBytes(data)))
	altOK := in.Family != "102" || in.Alt == "uint"
	return altOK && (in.Fields == "defList" || in.Fields == "indefList"), nil
}

type setContentInput struct {
	Where    string   `json:"where"`
	Content  string   `json:"content"`
	Members  []string `json:"members"`
	TreeOver bool     `json:"treeOver"`
}

// placeSetContent is ValueSetMembers.tla for the one set every transaction
// has, its inputs: the shape of what tag 258 holds. RefAccepts: the content
// is a list (IsList). The model's member kinds are maps keyed by arrays and
// maps, which only a governance proposal carries.
func placeSetContent(raw json.RawMessage, _ world, t *txSpec) (bool, error) {
	var in setContentInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return false, fmt.Errorf("decode ValueSetMembers input: %w", err)
	}
	if in.Where != "top" || len(in.Members) > 0 || in.TreeOver {
		return false, unmappable(-1, "set members with composite map keys need a governance proposal, which is not crafted")
	}
	shapes := map[string][]byte{
		"defList": cArray(inputHole), "indefList": cIndefArray(inputHole),
		"uint": cUint(1), "map": cMap(),
	}
	content, ok := shapes[in.Content]
	if !ok {
		return false, unmappable(-1, "unknown set content %q", in.Content)
	}
	t.Raw = map[uint64][]byte{bodyInputs: cTag(258, content)}
	return in.Content == "defList" || in.Content == "indefList", nil
}

type framingInput struct {
	Era     string `json:"era"`
	Framing string `json:"framing"`
}

// placeFraming is TxSizeFraming.tla's Framings: how the transaction's outer
// array is written. The model measures a Shelley to Mary transaction's size
// and takes for granted that every framing is a received transaction; only
// that premise is carried to Conway, where the size is the received bytes.
// The dingo model's eight-byte length is the same premise.
func placeFraming(raw json.RawMessage, _ world, t *txSpec) (bool, error) {
	var in framingInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return false, fmt.Errorf("decode TxSizeFraming input: %w", err)
	}
	switch in.Framing {
	case "definite":
		t.Frame = frameDefinite
	case "wide", "wide8":
		t.Frame = frameWide
	case "indefinite":
		t.Frame = frameIndefinite
	default:
		return false, unmappable(-1, "unknown framing %q", in.Framing)
	}
	return true, nil
}

// sorted returns cs in the order mistake.Expected wants its classes.
func sorted(cs []submit.Class) []submit.Class {
	out := slices.Clone(cs)
	slices.Sort(out)
	return out
}
