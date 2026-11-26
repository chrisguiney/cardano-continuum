package scenario

import (
	"encoding/json"
	"fmt"
	"slices"

	"github.com/blinklabs-io/gouroboros/ledger/common"

	"github.com/chrisguiney/cardano-continuum/internal/keys"
	"github.com/chrisguiney/cardano-continuum/internal/mistake"
	"github.com/chrisguiney/cardano-continuum/internal/submit"
	"github.com/chrisguiney/cardano-continuum/internal/tla"
)

// WitnessCase is one cell of Witnesses.tla in its environment, with the
// reference's verdict.
type WitnessCase struct {
	Era      string     `json:"era"`
	Mutation string     `json:"mutation,omitempty"`
	Env      tla.WitEnv `json:"env"`
	// Kind is the cell's family: key, native, script, datum or integrity.
	Kind    string          `json:"kind"`
	Cell    json.RawMessage `json:"cell"`
	Accepts bool            `json:"accepts"`
}

// keyCell is Witnesses.tla's KeyCells record.
type keyCell struct {
	Src   string `json:"src"`
	Key   string `json:"key"`
	Bad   bool   `json:"bad"`
	Valid bool   `json:"valid"`
}

// leaf and nativeCell are Witnesses.tla's Leaves and Scripts records.
type leaf struct {
	Op   string `json:"op"`
	Key  string `json:"key"`
	Slot int    `json:"slot"`
}

type nativeCell struct {
	Op   string `json:"op"`
	M    int    `json:"m"`
	Subs []leaf `json:"subs"`
}

// WitnessAdapter is the refinement mapping of Witnesses.tla's key-witness and
// native-script families (see the package documentation for the table).
type WitnessAdapter struct{}

// NewWitnessAdapter returns the ledger-witnesses adapter.
func NewWitnessAdapter() *WitnessAdapter { return &WitnessAdapter{} }

// Family implements Adapter.
func (*WitnessAdapter) Family() string { return FamilyWitnesses }

var witnessClasses = []submit.Class{
	submit.MissingVKeyWitnesses, submit.InvalidWitnesses,
	submit.ScriptWitnessNotValid, submit.MissingScriptWitnesses,
}

// Expected implements Adapter.
func (*WitnessAdapter) Expected(s Scenario, _ int) mistake.Expected {
	if s.Witness.Accepts {
		return mistake.Expected{Accept: true}
	}
	return mistake.Expected{Classes: sorted(witnessClasses)}
}

// keySources are the key-witness sources the mapping carries, and whether the
// key each needs is the actor's stake key rather than its payment key.
var keySources = map[string]bool{
	"in": false, "coll": false, "signer": false,
	"wdrl": true, "deleg": true, "reg0": true,
}

// witPrep is a cell read through the mapping.
type witPrep struct {
	w    world
	c    *WitnessCase
	key  *keyCell
	nat  *nativeCell
	unit uint64
}

// Applicable implements Adapter.
func (a *WitnessAdapter) Applicable(s Scenario, env Env) error {
	_, err := a.prepare(s, env)
	return err
}

func (a *WitnessAdapter) prepare(s Scenario, env Env) (witPrep, error) {
	c := s.Witness
	if c == nil {
		return witPrep{}, unmappable(-1, "scenario has no ledger-witnesses case")
	}
	if slices.Contains(c.Env.Wits, "b") {
		return witPrep{}, unmappable(-1, "a bootstrap witness needs a Byron address, which is not crafted")
	}
	w, err := newWorld(env)
	if err != nil {
		return witPrep{}, err
	}
	p := witPrep{w: w, c: c}
	switch c.Kind {
	case "key":
		p.key = new(keyCell)
		err = p.readKey(env.Caps)
	case "native":
		p.nat = new(nativeCell)
		err = p.readNative()
	default:
		err = unmappable(-1, "%s cells of Witnesses.tla are not concretized", c.Kind)
	}
	if err != nil {
		return witPrep{}, err
	}
	if p.unit, err = unitFor([]txOut{{Addr: w.lockAddr("key")}}, &env.Params); err != nil {
		return witPrep{}, err
	}
	return p, nil
}

func (p witPrep) readKey(caps Caps) error {
	if err := json.Unmarshal(p.c.Cell, p.key); err != nil {
		return fmt.Errorf("decode key cell: %w", err)
	}
	k := p.key
	_, ok := keySources[k.Src]
	switch {
	case !ok:
		return unmappable(-1, "key source %q (pools, genesis keys, voters) is not concretized", k.Src)
	case k.Key != "v1":
		return unmappable(-1, "key %q is a bootstrap key; Byron addresses are not crafted", k.Key)
	case !k.Valid && !caps.Phase2Invalid:
		return unmappable(-1, "target does not model phase-2-invalid transactions")
	}
	return nil
}

func (p witPrep) readNative() error {
	if err := json.Unmarshal(p.c.Cell, p.nat); err != nil {
		return fmt.Errorf("decode native script cell: %w", err)
	}
	e := p.c.Env
	if e.Lo >= 0 && e.Hi >= 0 && e.Lo >= e.Hi {
		return unmappable(-1, "validity interval [%d, %d) holds no slot, so the UTXO rule rejects before the script runs", e.Lo, e.Hi)
	}
	return nil
}

// Preconditions implements Adapter.
func (a *WitnessAdapter) Preconditions(s Scenario, env Env) (Preconditions, error) {
	p, err := a.prepare(s, env)
	if err != nil {
		return Preconditions{}, err
	}
	return Preconditions{
		Era: targetEra, ProtocolMajor: targetMajor, Params: pinParams(env.Params, p.unit),
		Funder: p.w.funder.Address(), Funds: 6 * p.unit, UTxOs: 2,
	}, nil
}

// Concretize implements Adapter. The setup transaction makes two outputs: the
// one the step spends (three units) and a spare of two units the "coll"
// source names as collateral.
func (a *WitnessAdapter) Concretize(s Scenario, env Env) (Plan, error) {
	p, err := a.prepare(s, env)
	if err != nil {
		return Plan{}, err
	}
	now := env.Start + 2
	setup, step := p.txs(now)
	raw, id := setup.encode()
	step.Inputs = []txIn{{ID: id}}
	if p.key != nil && p.key.Src == "coll" {
		step.Collateral = []txIn{{ID: id, Index: 1}}
	}
	tx, txID := step.encode()
	return Plan{Steps: []Step{
		{Kind: Submit, Note: "build the cell's outputs", Index: -1, Tx: raw, ID: id, Setup: true, Slot: env.Start, Expect: mistake.Expected{Accept: true}},
		{Kind: Wait, Note: "confirm the outputs", Index: -1, Slot: env.Start + 1},
		{Kind: Submit, Note: "offer the cell's transaction", Tx: tx, ID: txID, Slot: now, Expect: a.Expected(s, 0)},
	}}, nil
}

// txs returns the setup transaction and the cell's transaction, the latter
// without its inputs.
func (p witPrep) txs(now uint64) (setup, step txSpec) {
	u := p.unit
	funderKey := p.w.funder.PaymentKeyHash()
	home := p.w.keyAddr(funderKey)
	setup = txSpec{
		Inputs: []txIn{p.w.genesisRef()}, Fee: u,
		Outputs: []txOut{{Addr: home, Coin: 3 * u}, {Addr: p.w.lockAddr("key"), Coin: 2 * u}},
		Signers: []keys.Signer{p.w.funder.PaymentSigner()},
	}
	step = txSpec{Fee: u, Outputs: []txOut{{Addr: home, Coin: 2 * u}}}
	if p.nat != nil {
		p.native(&setup, &step, now)
	} else {
		p.keyed(&setup, &step)
	}
	if slices.Contains(p.c.Env.Wits, "v2") && !p.bad() {
		step.Signers = append(step.Signers, p.w.second.PaymentSigner())
	}
	if p.bad() {
		step.BadSigners = []keys.Signer{p.w.second.PaymentSigner()}
	}
	return setup, step
}

func (p witPrep) bad() bool { return p.key != nil && p.key.Bad }

// Conway certificate tags (conway.cddl).
const (
	certLegacyReg = 0
	certVoteDeleg = 9
)

// keyed fills in a key cell: the source that needs v1's witness, and the
// witnesses the environment supplies. The funder signs for the spent output
// unless the source is the input itself, so only the source decides whether
// v1 is needed.
func (p witPrep) keyed(setup, step *txSpec) {
	u, k := p.unit, p.key
	stake := p.w.scratch.StakeKeyHash()
	cred := cArray(cUint(0), cBytes(stake[:]))
	if k.Src == "in" {
		setup.Outputs[0].Addr = p.w.lockAddr("key")
	} else {
		step.Signers = append(step.Signers, p.w.funder.PaymentSigner())
	}
	switch k.Src {
	case "signer":
		h := p.w.scratch.PaymentKeyHash()
		step.ReqSigners = [][]byte{h[:]}
	case "wdrl", "deleg":
		// The account must exist, with a DRep, before it can be withdrawn
		// from or delegated again.
		setup.Certs = [][]byte{cArray(cUint(certVoteRegDeleg), cred, cArray(cUint(2)), cUint(u))}
		setup.Signers = append(setup.Signers, p.w.scratch.StakeSigner())
		setup.Outputs[1].Coin = u
		if k.Src == "wdrl" {
			step.Wdrl = p.w.rewardAddr()
		} else {
			step.Certs = [][]byte{cArray(cUint(certVoteDeleg), cred, cArray(cUint(2)))}
		}
	case "reg0":
		step.Certs = [][]byte{cArray(cUint(certLegacyReg), cred)}
		step.Outputs[0].Coin = u
	}
	if !slices.Contains(p.c.Env.Wits, "v1") {
		return
	}
	if keySources[k.Src] {
		step.Signers = append(step.Signers, p.w.scratch.StakeSigner())
	} else {
		step.Signers = append(step.Signers, p.w.scratch.PaymentSigner())
	}
}

// Native script tags (conway.cddl native_script).
const (
	nativeSig = iota
	nativeAll
	nativeAny
	nativeAtLeast
	nativeStart
	nativeExpire
)

// native fills in a native-script cell: the spent output is locked by the
// script, and the validity interval is the environment's bounds.
func (p witPrep) native(setup, step *txSpec, now uint64) {
	e := p.c.Env
	// base is the concrete slot of the model's slot 0, placed so the
	// interval holds the slot the transaction is validated at; one model
	// slot is one slot, so a leaf equal to a bound stays equal to it.
	base := now
	switch {
	case e.Lo >= 0:
		base = now - uint64(e.Lo)
	case e.Hi >= 0:
		base = now + 1 - uint64(e.Hi)
	}
	script := p.script(base)
	setup.Outputs[0].Addr = p.w.scriptAddr(nativeHash(script))
	step.Natives = [][]byte{script}
	if e.Lo >= 0 {
		lo := base + uint64(e.Lo)
		step.Start = &lo
	}
	if e.Hi >= 0 {
		hi := base + uint64(e.Hi)
		step.TTL = &hi
	}
	if slices.Contains(e.Wits, "v1") {
		step.Signers = append(step.Signers, p.w.scratch.PaymentSigner())
	}
}

func (p witPrep) script(base uint64) []byte {
	subs := make([][]byte, len(p.nat.Subs))
	for i, l := range p.nat.Subs {
		switch l.Op {
		case "sig":
			h := p.keyHash(l.Key)
			subs[i] = cArray(cUint(nativeSig), cBytes(h[:]))
		case "start":
			subs[i] = cArray(cUint(nativeStart), cUint(base+uint64(l.Slot)))
		default:
			subs[i] = cArray(cUint(nativeExpire), cUint(base+uint64(l.Slot)))
		}
	}
	switch p.nat.Op {
	case "all":
		return cArray(cUint(nativeAll), cArray(subs...))
	case "any":
		return cArray(cUint(nativeAny), cArray(subs...))
	}
	return cArray(cUint(nativeAtLeast), cUint(uint64(p.nat.M)), cArray(subs...))
}

// keyHash is the key hash a signature leaf names. The bootstrap key "b" has
// no witness in any environment the mapping carries, so any hash nobody signs
// for stands for it.
func (p witPrep) keyHash(name string) keys.Credential {
	switch name {
	case "v1":
		return p.w.scratch.PaymentKeyHash()
	case "v2":
		return p.w.second.PaymentKeyHash()
	}
	return keys.Credential(common.Blake2b224Hash([]byte("continuum/witnesses/bootstrap-root")))
}
