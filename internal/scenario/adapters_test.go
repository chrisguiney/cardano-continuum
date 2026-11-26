package scenario

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/blinklabs-io/gouroboros/ledger/conway"

	"github.com/chrisguiney/cardano-continuum/internal/fakechain"
	"github.com/chrisguiney/cardano-continuum/internal/submit"
	"github.com/chrisguiney/cardano-continuum/internal/tla"
)

func witnessScenario(kind string, env tla.WitEnv, accepts bool, cell any) Scenario {
	raw, _ := json.Marshal(cell)
	return Scenario{
		Version: FormatVersion, ID: "ledger-witnesses/test", Family: FamilyWitnesses, Source: SourceTrace,
		Witness: &WitnessCase{Era: "Conway", Env: env, Kind: kind, Cell: raw, Accepts: accepts},
	}
}

// TestKeyCells: each carried key source needs v1's witness and nothing else,
// and a bad extra signature rejects.
func TestKeyCells(t *testing.T) {
	for src := range keySources {
		for _, tc := range []struct {
			wits    []string
			bad     bool
			accepts bool
		}{
			{[]string{"v1"}, false, true},
			{[]string{"v2"}, false, src == "reg0"},
			{nil, false, src == "reg0"},
			{[]string{"v1", "v2"}, true, false},
		} {
			s := witnessScenario("key", tla.WitEnv{Wits: tc.wits, Lo: -1, Hi: -1}, tc.accepts,
				keyCell{Src: src, Key: "v1", Bad: tc.bad, Valid: true})
			if res := run(t, NewWitnessAdapter(), s); res.Status != Pass {
				t.Errorf("%s wits %v bad %v: %+v", src, tc.wits, tc.bad, res)
			}
		}
	}
}

// TestNativeExpiryOnTheBound: an expiry leaf equal to the upper bound holds
// (hi <= slot); one slot earlier it does not. The concrete script and
// interval keep that equality.
func TestNativeExpiryOnTheBound(t *testing.T) {
	for _, tc := range []struct {
		hi, leaf int
		accepts  bool
	}{{2, 2, true}, {2, 1, false}, {1, 2, true}} {
		cell := nativeCell{Op: "all", Subs: []leaf{{Op: "expire", Key: "v1", Slot: tc.leaf}}}
		s := witnessScenario("native", tla.WitEnv{Lo: -1, Hi: tc.hi}, tc.accepts, cell)
		if res := run(t, NewWitnessAdapter(), s); res.Status != Pass {
			t.Errorf("hi %d leaf %d: %+v", tc.hi, tc.leaf, res)
		}
		env := FakeBackend{}.Env()
		env.Seed, env.Start = testSeed(), 10
		plan, err := NewWitnessAdapter().Concretize(s, env)
		if err != nil {
			t.Fatal(err)
		}
		tx, err := conway.NewConwayTransactionFromCbor(plan.Steps[2].Tx)
		if err != nil {
			t.Fatal(err)
		}
		if tx.TTL() != plan.Steps[2].Slot+1 {
			t.Errorf("hi %d: invalid_hereafter %d, validated at %d", tc.hi, tx.TTL(), plan.Steps[2].Slot)
		}
	}
}

func TestNativeSignatureAndStart(t *testing.T) {
	sig := nativeCell{Op: "any", Subs: []leaf{{Op: "sig", Key: "v1"}, {Op: "sig", Key: "b"}}}
	start := nativeCell{Op: "atleast", M: 1, Subs: []leaf{{Op: "start", Key: "v1", Slot: 1}}}
	for name, s := range map[string]Scenario{
		"signed":       witnessScenario("native", tla.WitEnv{Wits: []string{"v1"}, Lo: -1, Hi: -1}, true, sig),
		"unsigned":     witnessScenario("native", tla.WitEnv{Wits: []string{"v2"}, Lo: -1, Hi: -1}, false, sig),
		"start met":    witnessScenario("native", tla.WitEnv{Lo: 1, Hi: -1}, true, start),
		"start early":  witnessScenario("native", tla.WitEnv{Lo: 0, Hi: 3}, false, start),
		"start absent": witnessScenario("native", tla.WitEnv{Lo: -1, Hi: -1}, false, start),
	} {
		if res := run(t, NewWitnessAdapter(), s); res.Status != Pass {
			t.Errorf("%s: %+v", name, res)
		}
	}
}

// TestUnmappableNamesTheReason: model state with no concrete form is reported
// with what it was, never run as a partial scenario.
func TestUnmappableNamesTheReason(t *testing.T) {
	plutus := baseTx("Babbage")
	plutus.Rdmrs, plutus.Coll = true, []string{"a"}
	spec := utxoScenario("Mary", oneOutput(0), tla.Verdict{Tx: baseTx("Mary"), Accepts: true})
	spec.Utxo.Mutation = "SpecMintIgnored"
	for want, s := range map[string]Scenario{
		"Byron":      witnessScenario("key", tla.WitEnv{Wits: []string{"b"}, Lo: -1, Hi: -1}, true, keyCell{Src: "in", Key: "v1", Valid: true}),
		`"owner"`:    witnessScenario("key", tla.WitEnv{Lo: -1, Hi: -1}, false, keyCell{Src: "owner", Key: "v1", Valid: true}),
		"datum":      witnessScenario("datum", tla.WitEnv{Lo: -1, Hi: -1}, false, map[string]any{}),
		"Plutus":     utxoScenario("Babbage", oneOutput(1), tla.Verdict{Tx: plutus, Accepts: true}),
		"reference":  spec,
		"CborRation": {Version: FormatVersion, ID: "codec/x", Family: FamilyCodec, Codec: &CodecCase{Model: "CborRational", Input: []byte(`{}`)}},
	} {
		res, err := Run(t.Context(), s, FakeBackend{}, testSeed())
		if err != nil {
			t.Fatal(err)
		}
		if res.Status != Unconcretizable || !strings.Contains(res.Reason, want) || res.Ran != 0 {
			t.Errorf("want unconcretizable naming %q, got %+v", want, res)
		}
	}
}

// TestRejectedStepSkippedAcceptedTruncates: a rejected step the mapping
// cannot carry is left out and the rest runs; an accepted one ends the plan.
func TestRejectedStepSkippedAcceptedTruncates(t *testing.T) {
	plutus := baseTx("Babbage")
	plutus.Rdmrs = true
	ok := baseTx("Babbage")
	after := tla.Ledger{UTxO: map[string]tla.Output{"a": key(2, 0)}, Slot: 1, Registered: true}
	again := tla.Verdict{Tx: ok, Accepts: true, After: &tla.Ledger{UTxO: map[string]tla.Output{"a": key(1, 0)}, Slot: 1, Registered: true}}
	s := utxoScenario("Babbage", oneOutput(1),
		tla.Verdict{Tx: plutus}, tla.Verdict{Tx: ok, Accepts: true, After: &after}, again,
		tla.Verdict{Tx: plutus, Accepts: true}, tla.Verdict{Tx: ok})
	res := run(t, NewUtxoAdapter(), s)
	if res.Status != Pass || res.Ran != 2 || len(res.Skipped) != 1 || res.Truncated == nil || res.Truncated.Index != 3 {
		t.Fatalf("result = %+v", res)
	}
}

// TestModelStateCrossChecked: a next ledger that is not what the mapping's
// arithmetic gives is an error, not a scenario.
func TestModelStateCrossChecked(t *testing.T) {
	wrong := tla.Ledger{UTxO: map[string]tla.Output{"a": key(3, 0)}, Slot: 1, Registered: true}
	s := utxoScenario("Babbage", oneOutput(1), tla.Verdict{Tx: baseTx("Babbage"), Accepts: true, After: &wrong}, tla.Verdict{Tx: baseTx("Babbage")})
	if _, err := Run(t.Context(), s, FakeBackend{}, testSeed()); err == nil || !strings.Contains(err.Error(), "mapping computed") {
		t.Fatalf("err = %v", err)
	}
}

func codecScenario(model string, input any) Scenario {
	raw, _ := json.Marshal(input)
	return Scenario{Version: FormatVersion, ID: "codec/test", Family: FamilyCodec, Source: SourceTable, Codec: &CodecCase{Model: model, Input: raw}}
}

// codecAnswer submits the case's transaction and returns the fake's answer.
func codecAnswer(t *testing.T, s Scenario) (Result, submit.Outcome) {
	t.Helper()
	res := run(t, NewCodecAdapter(), s)
	b := FakeBackend{}
	env := b.Env()
	env.Seed = testSeed()
	pre, err := NewCodecAdapter().Preconditions(s, env)
	if err != nil {
		t.Fatal(err)
	}
	tgt, err := b.Open(t.Context(), pre)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := NewCodecAdapter().Concretize(s, env)
	if err != nil {
		t.Fatal(err)
	}
	var out submit.Outcome
	for _, st := range plan.Steps {
		if st.Kind == Wait {
			if err := tgt.AdvanceTo(t.Context(), st.Slot); err != nil {
				t.Fatal(err)
			}
			continue
		}
		out = tgt.Submit(t.Context(), st.Tx)
	}
	return res, out
}

// TestCodecRejectedAsUndecodable: encodings the reference refuses are
// refused by the fake's decoder, with the deserialisation class.
func TestCodecRejectedAsUndecodable(t *testing.T) {
	for name, s := range map[string]Scenario{
		"inputs under tag 259":    codecScenario("PreConwaySetTag", setTagInput{Era: "Conway", Field: "inputs", Tag: "other"}),
		"tag 258 around a uint":   codecScenario("ValueSetMembers", setContentInput{Where: "top", Content: "uint"}),
		"constructor fields uint": codecScenario("PlutusConstrFields", constrInput{Family: "121-127", Alt: "uint", Fields: "uint"}),
	} {
		res, out := codecAnswer(t, s)
		if res.Status != Pass || len(res.Notes) != 0 {
			t.Errorf("%s: %+v", name, res)
		}
		if out.Verdict != submit.Rejected || len(out.Failures) != 1 || out.Failures[0].Class != submit.Undecodable {
			t.Errorf("%s: answer %+v", name, out)
		}
	}
}

// TestCodecDecodedThenRejectedIsADisagreement: bytes the reference cannot
// decode, rejected by a ledger rule, were decoded. A stand-in target that
// answers ValueNotConserved to everything shows the judgement.
func TestCodecDecodedThenRejectedIsADisagreement(t *testing.T) {
	s := codecScenario("PreConwaySetTag", setTagInput{Era: "Conway", Field: "inputs", Tag: "other"})
	res, err := runWith(t.Context(), s, NewCodecAdapter(), lenientBackend{}, testSeed())
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != Disagreement || len(res.Findings) != 1 || res.Findings[0].Kind != SpecDisagreement {
		t.Fatalf("result = %+v", res)
	}
}

// lenientBackend is the fake chain with a decoder that takes anything: every
// step transaction is answered with a ledger-rule rejection.
type lenientBackend struct{ FakeBackend }

func (b lenientBackend) Open(ctx context.Context, pre Preconditions) (Target, error) {
	t, err := b.FakeBackend.Open(ctx, pre)
	return &lenientTarget{Target: t}, err
}

type lenientTarget struct {
	Target
	sent int
}

func (t *lenientTarget) Submit(ctx context.Context, tx []byte) submit.Outcome {
	t.sent++
	if t.sent == 1 {
		return t.Target.Submit(ctx, tx)
	}
	return submit.Outcome{Target: "lenient", Verdict: submit.Rejected, Failures: []submit.Failure{{Class: submit.ValueNotConserved}}}
}

// TestCodecAcceptedForms: forms the reference decodes are accepted outright
// when the transaction around them is valid.
func TestCodecAcceptedForms(t *testing.T) {
	for name, s := range map[string]Scenario{
		"wide outer array":       codecScenario("TxSizeFraming", framingInput{Framing: "wide"}),
		"indefinite outer array": codecScenario("TxSizeFraming", framingInput{Framing: "indefinite"}),
		"inputs under tag 258":   codecScenario("PreConwaySetTag", setTagInput{Era: "Conway", Field: "inputs", Tag: "258"}),
		"untagged inputs":        codecScenario("PreConwaySetTag", setTagInput{Era: "Conway", Field: "inputs", Tag: "none"}),
		"indefinite fields":      codecScenario("PlutusConstrFields", constrInput{Family: "102", Alt: "uint", Fields: "indefList"}),
	} {
		res, out := codecAnswer(t, s)
		if res.Status != Pass || out.Verdict != submit.Accepted {
			t.Errorf("%s: %+v answer %+v", name, res, out)
		}
	}
}

func TestQuantityEncodings(t *testing.T) {
	for _, tc := range []struct {
		value, enc, hex string
	}{
		{"five", "head", "05"}, {"five", "bignum", "c24105"}, {"five", "bignumLongTag", "d8024105"},
		{"minusOne", "head", "20"}, {"minusOne", "bignum", "c340"},
		{"w64max", "head", "1bffffffffffffffff"}, {"i64minMinus1", "head", "3b8000000000000000"},
	} {
		got, err := encodeQuantity(quantities[tc.value], tc.enc)
		if err != nil || !bytes.Equal(got, mustHex(t, tc.hex)) {
			t.Errorf("%s %s = %x (%v), want %s", tc.value, tc.enc, got, err, tc.hex)
		}
	}
	if _, err := encodeQuantity(quantities["beyond64"], "head"); err == nil {
		t.Error("2^64 written as a head")
	}
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	var out []byte
	for i := 0; i+1 < len(s); i += 2 {
		var b byte
		for _, c := range []byte(s[i : i+2]) {
			b <<= 4
			switch {
			case c >= '0' && c <= '9':
				b |= c - '0'
			default:
				b |= c - 'a' + 10
			}
		}
		out = append(out, b)
	}
	return out
}

// TestFaultStep: a plan may inject a fake-chain fault between submissions.
func TestFaultStep(t *testing.T) {
	b := FakeBackend{}
	env := b.Env()
	env.Seed = testSeed()
	s := utxoScenario("Babbage", oneOutput(1), tla.Verdict{Tx: baseTx("Babbage"), Accepts: true})
	pre, err := NewUtxoAdapter().Preconditions(s, env)
	if err != nil {
		t.Fatal(err)
	}
	tgt, err := b.Open(t.Context(), pre)
	if err != nil {
		t.Fatal(err)
	}
	res := Result{}
	x := executor{s: s, t: tgt, res: &res}
	if _, err := x.step(t.Context(), Step{Kind: Fault, Fault: fakechain.Delay{Slots: 3}}); err != nil {
		t.Fatal(err)
	}
	if _, err := x.step(t.Context(), Step{Kind: Wait, Slot: 2}); err != nil {
		t.Fatal(err)
	}
	if tgt.Slot() != 0 {
		t.Fatalf("delayed chain validates at slot %d, want 0: no block yet", tgt.Slot())
	}
	if _, err := x.step(t.Context(), Step{Kind: "bogus"}); err == nil {
		t.Error("unknown step kind accepted")
	}
}

func TestLibraryRoundTrip(t *testing.T) {
	dir := t.TempDir()
	want := utxoScenario("Allegra", oneOutput(1), tla.Verdict{Tx: baseTx("Allegra"), Accepts: true})
	want.ID = "ledger-utxo/UtxoX/0"
	if err := Save(dir, want); err != nil {
		t.Fatal(err)
	}
	for _, arg := range []string{"ledger-utxo/UtxoX/0", "ledger-utxo", dir} {
		got, err := Find(dir, arg)
		if err != nil || len(got) != 1 || !reflect.DeepEqual(got[0], want) {
			t.Fatalf("Find(%q) = %+v, %v", arg, got, err)
		}
	}
	if _, err := Find(dir, "ledger-utxo/none"); err == nil {
		t.Error("missing id found")
	}
	if _, err := Find(dir, "../x"); err == nil {
		t.Error("path escape accepted as an id")
	}
}

func TestDecodeRejects(t *testing.T) {
	for name, text := range map[string]string{
		"other version": `{"version":2,"id":"a/b","family":"codec","source":"table","provenance":{"repo":"","commit":"","spec":""},"codec":{"model":"m","input":{}}}`,
		"unknown field": `{"version":1,"id":"a/b","surprise":1}`,
		"no case":       `{"version":1,"id":"a/b","family":"codec","source":"table","provenance":{"repo":"","commit":"","spec":""}}`,
		"bad id":        `{"version":1,"id":"../b","family":"codec","source":"table","provenance":{"repo":"","commit":"","spec":""},"codec":{"model":"m","input":{}}}`,
	} {
		if _, err := Decode(strings.NewReader(text)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// TestCuratedLibrary runs the committed scenarios. Every one gets the
// model's verdict from the fake chain except those knownDisagreements lists.
func TestCuratedLibrary(t *testing.T) {
	ss, err := Load(filepath.Join("..", "..", "scenarios"))
	if err != nil {
		t.Fatal(err)
	}
	if len(ss) < 8 {
		t.Fatalf("library has %d scenarios", len(ss))
	}
	for _, s := range ss {
		if s.Prov.Commit == "" || s.Prov.Spec == "" || s.Prov.Repo == "" {
			t.Errorf("%s: provenance incomplete: %+v", s.ID, s.Prov)
		}
		res, err := Run(t.Context(), s, FakeBackend{}, testSeed())
		if err != nil {
			t.Fatalf("%s: %v", s.ID, err)
		}
		want := Pass
		if knownDisagreements[s.ID] {
			want = Disagreement
		}
		if res.Status != want {
			t.Errorf("%s: status %s (%s), want %s", s.ID, res.Status, res.Reason, want)
		}
	}
}

// knownDisagreements are curated scenarios the fake chain answers against
// the model: see scenarios/README.md.
var knownDisagreements = map[string]bool{
	"codec/MultiAssetQuantityMainBug":                               true,
	"codec/table/MultiAssetQuantity/outputAsset-five-bignumLongTag": true,
}

func TestSampleVectorsDeterministic(t *testing.T) {
	var buf bytes.Buffer
	var vs []tla.Vector
	for i := range 50 {
		tx := baseTx("Allegra")
		tx.Hi = i % 4
		vs = append(vs, tla.Vector{Era: "Allegra", Family: "interval", Ledger: oneOutput(1), Tx: tx, SpecAccepts: tx.Hi == 0 || tx.Hi > 1})
	}
	if err := tla.WriteVectors(&buf, vs); err != nil {
		t.Fatal(err)
	}
	sample := func(seed byte) []Scenario {
		ss, err := SampleVectors(bytes.NewReader(buf.Bytes()), 10, testSeed().Derive(string(seed)).Rand(), "repo", "c0ffee")
		if err != nil {
			t.Fatal(err)
		}
		return ss
	}
	a, b, c := sample(1), sample(1), sample(2)
	if len(a) != 10 || !reflect.DeepEqual(a, b) || reflect.DeepEqual(a, c) {
		t.Fatalf("sample sizes %d %d, same-seed equal %v", len(a), len(b), reflect.DeepEqual(a, b))
	}
	for _, s := range a {
		if res := run(t, NewUtxoAdapter(), s); res.Status != Pass {
			t.Errorf("%s: %+v", s.ID, res)
		}
	}
}

func TestSummary(t *testing.T) {
	sum := Summarize([]Result{
		{Family: FamilyUtxo, Source: SourceTrace, Status: Pass, Steps: 3, Skipped: []Skip{{1, "why"}}},
		{Family: FamilyUtxo, Source: SourceTrace, Status: Unconcretizable, Reason: "no form"},
		{Family: FamilyCodec, Source: SourceTable, Status: Disagreement},
	})
	if got := sum.Total(); got != (Tally{Run: 3, Pass: 1, Disagreement: 1, Unconcretizable: 1, StepsSkipped: 1}) {
		t.Fatalf("total = %+v", got)
	}
	var buf bytes.Buffer
	if err := sum.Write(&buf); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ledger-utxo trace", "spec_disagreement", "no form", "why"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("summary lacks %q:\n%s", want, buf.String())
		}
	}
}

func TestOwners(t *testing.T) {
	for fam, want := range map[string]Owner{
		"chainsync": OwnerRoguePeer, "consensus-nonce": OwnerRoguePool, "ledger-utxo": OwnerTxCrafter,
		"cbor-codec": OwnerMalformedTx, "ledger-governance": OwnerLater, "engine": OwnerNone,
	} {
		if got := OwnerOf(fam); got != want {
			t.Errorf("OwnerOf(%s) = %s, want %s", fam, got, want)
		}
	}
}
